// Command estres is a staged load test for RutaCree. It simulates buses whose drivers report GPS
// every 3 s (and acknowledge ramp requests on the driver socket), and riders who fetch the routes,
// keep the live map open on /api/socket and sometimes request the ramp and poll its status, just
// like the app. Riders are added step by step; each step is held and checked against targets
// (errors, latency, dropped sockets). The last step that passes is the server's capacity.
//
// Run it against the replica (estres/README.md), never against production at full load: the
// per-IP limits would only measure the rate limiter, and real riders would suffer.
//
//	go run ./estres -url https://localhost:8443 -insecure -spoof \
//	    -admin-pass-file secrets/admin_password -steps 250,500,1000,1500,2000
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
)

var (
	baseURL    = flag.String("url", "https://localhost:8443", "API base URL")
	insecure   = flag.Bool("insecure", false, "accept the replica's self-signed certificate")
	spoof      = flag.Bool("spoof", false, "send a random X-Forwarded-For per rider (replica only: needs trusted proxies)")
	adminEmail = flag.String("admin-email", "admin@rutacree.mx", "panel admin used to create the test drivers")
	adminPass  = flag.String("admin-pass-file", "secrets/admin_password", "file with that admin's password")
	nBuses     = flag.Int("buses", 30, "simulated buses on shift (0 = riders only)")
	gpsEvery   = flag.Duration("gps-every", 3*time.Second, "driver position interval (the app uses 3 s)")
	stepsFlag  = flag.String("steps", "100,250,500,1000,1500,2000,3000", "simultaneous riders per step")
	rampUp     = flag.Duration("ramp", 30*time.Second, "time to add the riders of a step")
	hold       = flag.Duration("hold", 60*time.Second, "time each step is held and measured")
	session    = flag.Duration("session", 5*time.Minute, "mean rider session; then the app closes and another opens (0 = never)")
	rampRate   = flag.Float64("ramp-rate", 0.02, "ramp requests per rider per minute (0 = none)")
	statsCmd   = flag.String("stats-cmd", "", `command printing "name cpu% mem" lines, sampled during the test (e.g. docker stats)`)
	keepGoing  = flag.Bool("keep-going", false, "run every step even after one fails")
	outFile    = flag.String("out", "", "also write the results as JSON here")

	// Demo mode just shows the fleet on the panel: start -buses paced (so a single IP doesn't trip
	// the login/rate limits), report positions every -gps-every, keep the shifts open until Ctrl-C,
	// and close them on exit. No riders, no driver sockets, no measurement.
	demo = flag.Bool("demo", false, "only drive the fleet so it shows on the panel; hold until interrupted")
	pace = flag.Duration("pace", 7*time.Second, "demo: delay between starting each bus (login limit is 10/min)")
)

// Targets a step must meet to count as supported.
var (
	maxErrRate     = flag.Float64("slo-err", 0.01, "max error rate (HTTP and socket connects)")
	maxHTTPp95     = flag.Duration("slo-http", time.Second, "max HTTP p95")
	maxDeliveryP95 = flag.Duration("slo-delivery", 2500*time.Millisecond, "max p95 from GPS report to the rider's screen")
)

const password = "Estres-Carga-2026" // test drivers only (LT-xx)

func main() {
	flag.Parse()
	steps, err := parseSteps(*stepsFlag)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cat, err := loadCatalog(ctx)
	if err != nil {
		log.Fatalf("rutas: %v", err)
	}
	log.Printf("%d rutas, %d paradas accesibles", len(cat.routes), len(cat.accessible))

	if *demo {
		runDemo(ctx, cat)
		return
	}

	var buses []*bus
	if *nBuses > 0 {
		if buses, err = startBuses(ctx, cat); err != nil {
			log.Fatalf("choferes: %v", err)
		}
		defer endShifts(buses)
		for _, b := range buses {
			go b.run(ctx, true)
		}
		log.Printf("%d unidades en turno reportando cada %v", len(buses), *gpsEvery)
	}
	if *statsCmd != "" {
		go sampleStats(ctx)
	}

	var results []stepResult
	running := 0
	riderCtx, stopRiders := context.WithCancel(ctx)
	defer stopRiders()
	for _, target := range steps {
		if ctx.Err() != nil {
			break
		}
		add := target - running
		log.Printf("subiendo a %d pasajeros (+%d en %v)", target, add, *rampUp)
		for i := 0; i < add && ctx.Err() == nil; i++ {
			go rider(riderCtx, cat, running+i)
			time.Sleep(*rampUp / time.Duration(max(add, 1)))
		}
		running = target
		time.Sleep(5 * time.Second) // let the last connections settle before measuring
		m.reset()
		select {
		case <-ctx.Done():
		case <-time.After(*hold):
		}
		r := m.result(target)
		results = append(results, r)
		printRow(r)
		if !r.Pass && !*keepGoing {
			log.Printf("el paso de %d pasajeros no cumplió: %s", target, strings.Join(r.Failures, "; "))
			break
		}
	}
	stopRiders()
	summary(results)
	if *outFile != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*outFile, b, 0o644); err != nil {
			log.Print(err)
		}
	}
}

func parseSteps(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n <= 0 || (len(out) > 0 && n <= out[len(out)-1]) {
			return nil, fmt.Errorf("pasos inválidos %q: enteros crecientes", s)
		}
		out = append(out, n)
	}
	return out, nil
}

// ---- metrics ----

// hist counts samples per millisecond up to one minute; percentiles come from the cumulative sum.
type hist struct{ b [60001]atomic.Int64 }

func (h *hist) add(d time.Duration) {
	ms := min(max(d.Milliseconds(), 0), int64(len(h.b)-1))
	h.b[ms].Add(1)
}

func (h *hist) reset() {
	for i := range h.b {
		h.b[i].Store(0)
	}
}

func (h *hist) pct(ps ...float64) (n int64, out []float64) {
	counts := make([]int64, len(h.b))
	for i := range h.b {
		counts[i] = h.b[i].Load()
		n += counts[i]
	}
	out = make([]float64, len(ps))
	if n == 0 {
		return 0, out
	}
	for j, p := range ps {
		want, acc := int64(math.Ceil(p*float64(n))), int64(0)
		for i, c := range counts {
			if acc += c; acc >= want {
				out[j] = float64(i)
				break
			}
		}
	}
	return n, out
}

type metrics struct {
	http, delivery, connect hist
	httpReq, httpErr        atomic.Int64
	wsTry, wsErr            atomic.Int64
	drops, msgs, bytes      atomic.Int64
	active                  atomic.Int64 // open rider sockets right now
	gpsSent, gpsErr         atomic.Int64
	since                   atomic.Int64 // unix nanos of the last reset

	mu      sync.Mutex
	reasons map[string]int // why sockets dropped or failed, during the window
	stats   map[string]*usage
}

type usage struct{ peakCPU, peakMem, sumCPU, n float64 }

var m = &metrics{reasons: map[string]int{}, stats: map[string]*usage{}}

func (m *metrics) reset() {
	m.http.reset()
	m.delivery.reset()
	m.connect.reset()
	for _, c := range []*atomic.Int64{&m.httpReq, &m.httpErr, &m.wsTry, &m.wsErr, &m.drops, &m.msgs, &m.bytes, &m.gpsSent, &m.gpsErr} {
		c.Store(0)
	}
	m.mu.Lock()
	clear(m.reasons)
	clear(m.stats)
	m.mu.Unlock()
	m.since.Store(time.Now().UnixNano())
}

func (m *metrics) reason(r string) {
	if len(r) > 80 {
		r = r[:80]
	}
	m.mu.Lock()
	m.reasons[r]++
	m.mu.Unlock()
}

type stepResult struct {
	Riders        int                   `json:"riders"`
	Active        int64                 `json:"activeSockets"`
	Seconds       float64               `json:"seconds"`
	HTTPReqPerSec float64               `json:"httpReqPerSec"`
	HTTPErrRate   float64               `json:"httpErrRate"`
	HTTPp50       float64               `json:"httpP50Ms"`
	HTTPp95       float64               `json:"httpP95Ms"`
	HTTPp99       float64               `json:"httpP99Ms"`
	WSErrRate     float64               `json:"wsConnectErrRate"`
	Connectp95    float64               `json:"wsConnectP95Ms"`
	Drops         int64                 `json:"droppedSockets"`
	Deliveryp50   float64               `json:"deliveryP50Ms"`
	Deliveryp95   float64               `json:"deliveryP95Ms"`
	Deliveryp99   float64               `json:"deliveryP99Ms"`
	MsgsPerSec    float64               `json:"wsMsgsPerSec"`
	MbitPerSec    float64               `json:"wsMbitPerSec"` // payload only, before TLS/framing
	GPSErr        int64                 `json:"gpsErrors"`
	Reasons       map[string]int        `json:"errors,omitempty"`
	Server        map[string][3]float64 `json:"server,omitempty"` // name -> peak CPU %, avg CPU %, peak MiB
	Pass          bool                  `json:"pass"`
	Failures      []string              `json:"failures,omitempty"`
}

func rate(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (m *metrics) result(target int) stepResult {
	secs := time.Since(time.Unix(0, m.since.Load())).Seconds()
	r := stepResult{Riders: target, Active: m.active.Load(), Seconds: secs}
	var p []float64
	_, p = m.http.pct(.5, .95, .99)
	r.HTTPp50, r.HTTPp95, r.HTTPp99 = p[0], p[1], p[2]
	_, p = m.delivery.pct(.5, .95, .99)
	r.Deliveryp50, r.Deliveryp95, r.Deliveryp99 = p[0], p[1], p[2]
	_, p = m.connect.pct(.95)
	r.Connectp95 = p[0]
	r.HTTPReqPerSec = float64(m.httpReq.Load()) / secs
	r.HTTPErrRate = rate(m.httpErr.Load(), m.httpReq.Load())
	r.WSErrRate = rate(m.wsErr.Load(), m.wsTry.Load())
	r.Drops = m.drops.Load()
	r.MsgsPerSec = float64(m.msgs.Load()) / secs
	r.MbitPerSec = float64(m.bytes.Load()) * 8 / secs / 1e6
	r.GPSErr = m.gpsErr.Load()
	m.mu.Lock()
	r.Reasons = copyMap(m.reasons)
	if len(m.stats) > 0 {
		r.Server = map[string][3]float64{}
		for k, u := range m.stats {
			r.Server[k] = [3]float64{u.peakCPU, u.sumCPU / u.n, u.peakMem}
		}
	}
	m.mu.Unlock()

	fail := func(f string, a ...any) { r.Failures = append(r.Failures, fmt.Sprintf(f, a...)) }
	if r.HTTPErrRate > *maxErrRate {
		fail("errores HTTP %.1f%%", r.HTTPErrRate*100)
	}
	if r.WSErrRate > *maxErrRate {
		fail("conexiones de socket fallidas %.1f%%", r.WSErrRate*100)
	}
	if r.HTTPp95 > float64(maxHTTPp95.Milliseconds()) {
		fail("HTTP p95 %.0f ms", r.HTTPp95)
	}
	if *nBuses > 0 && r.Deliveryp95 > float64(maxDeliveryP95.Milliseconds()) {
		fail("entrega de posiciones p95 %.0f ms", r.Deliveryp95)
	}
	if float64(r.Drops) > *maxErrRate*float64(target) {
		fail("%d sockets caídos", r.Drops)
	}
	if float64(r.Active) < (1-*maxErrRate)*float64(target) {
		fail("solo %d sockets abiertos", r.Active)
	}
	if r.GPSErr > 0 {
		fail("%d reportes GPS rechazados", r.GPSErr)
	}
	r.Pass = len(r.Failures) == 0
	return r
}

func copyMap(src map[string]int) map[string]int {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func printRow(r stepResult) {
	verdict := "OK"
	if !r.Pass {
		verdict = "FALLA"
	}
	log.Printf("%s %d pasajeros: sockets=%d http=%.0freq/s err=%.2f%% p95=%.0fms | ws err=%.2f%% caídos=%d | entrega p50/p95/p99=%.0f/%.0f/%.0fms | %.0f msg/s %.1f Mbit/s",
		verdict, r.Riders, r.Active, r.HTTPReqPerSec, r.HTTPErrRate*100, r.HTTPp95, r.WSErrRate*100, r.Drops,
		r.Deliveryp50, r.Deliveryp95, r.Deliveryp99, r.MsgsPerSec, r.MbitPerSec)
	if len(r.Server) > 0 {
		names := make([]string, 0, len(r.Server))
		for k := range r.Server {
			names = append(names, k)
		}
		sort.Strings(names)
		var parts []string
		for _, k := range names {
			s := r.Server[k]
			parts = append(parts, fmt.Sprintf("%s cpu %.0f%% (pico %.0f%%) %.0fMiB", k, s[1], s[0], s[2]))
		}
		log.Printf("   servidor: %s", strings.Join(parts, " | "))
	}
	if len(r.Reasons) > 0 {
		log.Printf("   errores: %v", r.Reasons)
	}
}

func summary(rs []stepResult) {
	if len(rs) == 0 {
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "pasajeros\tsockets\treq/s\terr HTTP\tHTTP p95\tws err\tcaídos\tentrega p95\tMbit/s\tresultado\t")
	best := 0
	for _, r := range rs {
		v := "OK"
		if !r.Pass {
			v = "FALLA"
		} else {
			best = r.Riders
		}
		fmt.Fprintf(w, "%d\t%d\t%.0f\t%.2f%%\t%.0f ms\t%.2f%%\t%d\t%.0f ms\t%.1f\t%s\t\n", r.Riders, r.Active,
			r.HTTPReqPerSec, r.HTTPErrRate*100, r.HTTPp95, r.WSErrRate*100, r.Drops, r.Deliveryp95, r.MbitPerSec, v)
	}
	w.Flush()
	fmt.Printf("\nCapacidad medida: %d pasajeros simultáneos cumpliendo los objetivos.\n", best)
}

// sampleStats runs -stats-cmd in a loop and keeps per-container CPU/memory for the current window.
func sampleStats(ctx context.Context) {
	for ctx.Err() == nil {
		out, err := exec.CommandContext(ctx, "sh", "-c", *statsCmd).Output()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 3 {
				continue
			}
			cpu, err := strconv.ParseFloat(strings.TrimSuffix(f[1], "%"), 64)
			if err != nil {
				continue
			}
			mem := parseMiB(f[2])
			m.mu.Lock()
			u := m.stats[f[0]]
			if u == nil {
				u = &usage{}
				m.stats[f[0]] = u
			}
			u.peakCPU, u.peakMem = max(u.peakCPU, cpu), max(u.peakMem, mem)
			u.sumCPU += cpu
			u.n++
			m.mu.Unlock()
		}
	}
}

func parseMiB(s string) float64 {
	units := []struct {
		suf string
		mul float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"GB", 1000}, {"MB", 1}, {"kB", 1.0 / 1000}, {"B", 1.0 / (1 << 20)}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, u.suf), 64)
			return v * u.mul
		}
	}
	return 0
}

// ---- HTTP ----

func newTransport(h2 bool) *http.Transport {
	return &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: *insecure},
		ForceAttemptHTTP2:   h2,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
}

// client is one phone: its own connections and, with -spoof, its own public IP.
type client struct {
	http *http.Client
	ws   *http.Client
	ip   string
}

func newClient() *client {
	c := &client{
		http: &http.Client{Transport: newTransport(true), Timeout: 20 * time.Second},
		ws:   &http.Client{Transport: newTransport(false)},
	}
	if *spoof {
		c.ip = fmt.Sprintf("10.%d.%d.%d", rand.IntN(256), rand.IntN(256), 1+rand.IntN(254))
	}
	return c
}

func (c *client) close() {
	c.http.CloseIdleConnections()
	c.ws.CloseIdleConnections()
}

// do sends a request and records it; `measure` false skips the metrics (setup calls).
func (c *client) do(ctx context.Context, method, path, token string, body, out any, measure bool) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, *baseURL+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.ip != "" {
		req.Header.Set("X-Forwarded-For", c.ip)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		if measure && ctx.Err() == nil {
			m.httpReq.Add(1)
			m.httpErr.Add(1)
			m.reason("http: " + shortErr(err))
		}
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if measure {
		m.httpReq.Add(1)
		m.http.add(time.Since(start))
		if resp.StatusCode >= 400 {
			m.httpErr.Add(1)
			m.reason(fmt.Sprintf("%s %s → %d", method, routeOf(path), resp.StatusCode))
		}
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(data))
	}
	if out != nil && len(data) > 0 {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

func routeOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		if _, err := strconv.Atoi(path[i+1:]); err == nil {
			return path[:i] + "/{id}"
		}
	}
	return path
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

func (c *client) dial(ctx context.Context, path, token string) (*websocket.Conn, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	if c.ip != "" {
		h.Set("X-Forwarded-For", c.ip)
	}
	u := "ws" + strings.TrimPrefix(*baseURL, "http") + path
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(dctx, u, &websocket.DialOptions{HTTPClient: c.ws, HTTPHeader: h})
	if err != nil && resp != nil {
		err = fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return conn, err
}

// ---- routes ----

type catalog struct {
	routes     []route
	accessible []stopRef
	etag       string
}

type route struct {
	id   string
	line [][2]float64 // lon, lat
}

type stopRef struct{ route, stop string }

func loadCatalog(ctx context.Context) (*catalog, error) {
	var body struct {
		Routes []struct {
			Features []struct {
				Properties map[string]any `json:"properties"`
				Geometry   struct {
					Type        string          `json:"type"`
					Coordinates json.RawMessage `json:"coordinates"`
				} `json:"geometry"`
			} `json:"features"`
		} `json:"routes"`
	}
	c := newClient()
	if _, err := c.do(ctx, "GET", "/api/v1/routes", "", nil, &body, false); err != nil {
		return nil, err
	}
	cat := &catalog{}
	for _, r := range body.Routes {
		var rt route
		var stops []string
		for _, f := range r.Features {
			switch f.Geometry.Type {
			case "LineString":
				rt.id, _ = f.Properties["id"].(string)
				json.Unmarshal(f.Geometry.Coordinates, &rt.line)
			case "Point":
				if ok, _ := f.Properties["accessible"].(bool); ok {
					id, _ := f.Properties["id"].(string)
					stops = append(stops, id)
				}
			}
		}
		if rt.id == "" || len(rt.line) < 2 {
			continue
		}
		cat.routes = append(cat.routes, rt)
		for _, s := range stops {
			cat.accessible = append(cat.accessible, stopRef{rt.id, s})
		}
	}
	if len(cat.routes) == 0 {
		return nil, errors.New("el servidor no tiene rutas activas")
	}
	// The app keeps the ETag; returning riders send it and get a 304.
	req, _ := http.NewRequestWithContext(ctx, "HEAD", *baseURL+"/api/v1/routes", nil)
	if resp, err := c.http.Do(req); err == nil {
		cat.etag = resp.Header.Get("ETag")
		resp.Body.Close()
	}
	return cat, nil
}

// ---- buses (drivers) ----

type bus struct {
	c      *client
	number string
	route  route
	token  string // shift token
	id     uint64

	mu   sync.Mutex
	sent map[float64]time.Time // latitude -> when it was reported (to time its arrival at riders)
}

var busesByID sync.Map // uint64 -> *bus

func startBuses(ctx context.Context, cat *catalog) ([]*bus, error) {
	admin := newClient()
	if *spoof {
		admin.ip = "10.255.0.1"
	}
	pass, err := os.ReadFile(*adminPass)
	if err != nil {
		return nil, err
	}
	var login struct{ Token string }
	if _, err := admin.do(ctx, "POST", "/api/v1/admin/login", "",
		map[string]string{"email": *adminEmail, "password": strings.TrimSpace(string(pass))}, &login, false); err != nil {
		return nil, fmt.Errorf("login del admin: %w", err)
	}

	var buses []*bus
	for i := 1; i <= *nBuses; i++ {
		id := fmt.Sprintf("LT-%03d", i)
		email := fmt.Sprintf("lt%03d@estres.test", i)
		code, err := admin.do(ctx, "POST", "/api/v1/admin/drivers", login.Token, map[string]string{
			"id": id, "name": "Chofer de prueba " + id, "email": email, "password": password,
		}, nil, false)
		if err != nil && code != http.StatusConflict {
			return buses, fmt.Errorf("alta de %s: %w", id, err)
		}
		b := &bus{c: newClient(), number: fmt.Sprintf("LT%03d", i), route: cat.routes[(i-1)%len(cat.routes)], sent: map[float64]time.Time{}}
		if *spoof {
			b.c.ip = fmt.Sprintf("10.254.0.%d", i%250+1)
		}
		var dl struct{ Token string }
		if _, err := b.c.do(ctx, "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": password}, &dl, false); err != nil {
			return buses, fmt.Errorf("login de %s: %w", id, err)
		}
		var sh struct {
			Token string
			BusID uint64 `json:"busId"`
		}
		if _, err := b.c.do(ctx, "POST", "/api/v1/driver/shifts", dl.Token,
			map[string]string{"busNumber": b.number, "routeId": b.route.id}, &sh, false); err != nil {
			return buses, fmt.Errorf("turno de %s: %w", id, err)
		}
		b.token, b.id = sh.Token, sh.BusID
		busesByID.Store(b.id, b)
		buses = append(buses, b)
	}
	return buses, nil
}

func endShifts(buses []*bus) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, b := range buses {
		b.c.do(ctx, "POST", "/api/v1/driver/shifts/end", b.token, map[string]string{"finalLocation": "terminal"}, nil, false)
	}
	log.Printf("turnos de prueba cerrados")
}

// runDemo drives the fleet against a real server so it shows on the panel. Buses are started one
// by one at -pace (the login limit is 10/min, so 150 buses take a while); a 429 just means "slow
// down", so the setup waits and retries instead of failing. The shifts stay open until Ctrl-C.
func runDemo(ctx context.Context, cat *catalog) {
	admin := newClient()
	pass, err := os.ReadFile(*adminPass)
	if err != nil {
		log.Fatal(err)
	}
	var login struct{ Token string }
	if err := admin.doRetry(ctx, "POST", "/api/v1/admin/login", "",
		map[string]string{"email": *adminEmail, "password": strings.TrimSpace(string(pass))}, &login); err != nil {
		log.Fatalf("login del admin: %v", err)
	}

	var buses []*bus
	defer func() { endShifts(buses) }()
	log.Printf("dando de alta %d unidades (una cada ~%v; tomará unos %v)…",
		*nBuses, *pace, time.Duration(*nBuses)*(*pace))
	for i := 1; i <= *nBuses && ctx.Err() == nil; i++ {
		id := fmt.Sprintf("LT-%03d", i)
		email := fmt.Sprintf("lt%03d@estres.test", i)
		if err := admin.doRetry(ctx, "POST", "/api/v1/admin/drivers", login.Token, map[string]string{
			"id": id, "name": "DEMO " + id, "email": email, "password": password,
		}, nil); err != nil {
			log.Printf("alta de %s: %v", id, err)
			continue
		}
		b := &bus{c: newClient(), number: fmt.Sprintf("LT%03d", i), route: cat.routes[(i-1)%len(cat.routes)], sent: map[float64]time.Time{}}
		var dl struct{ Token string }
		if err := b.c.doRetry(ctx, "POST", "/api/v1/auth/login", "", map[string]string{"email": email, "password": password}, &dl); err != nil {
			log.Printf("login de %s: %v", id, err)
			continue
		}
		var sh struct {
			Token string
			BusID uint64 `json:"busId"`
		}
		if err := b.c.doRetry(ctx, "POST", "/api/v1/driver/shifts", dl.Token,
			map[string]string{"busNumber": b.number, "routeId": b.route.id}, &sh); err != nil {
			log.Printf("turno de %s: %v", id, err)
			continue
		}
		b.token, b.id = sh.Token, sh.BusID
		busesByID.Store(b.id, b)
		buses = append(buses, b)
		go b.run(ctx, false)
		if len(buses)%10 == 0 || len(buses) == *nBuses {
			log.Printf("%d/%d unidades en turno y moviéndose en Durango", len(buses), *nBuses)
		}
		sleep(ctx, *pace)
	}
	if ctx.Err() != nil {
		return
	}
	log.Printf("LISTO: %d unidades activas. Míralas en el panel → /#/monitoreo. Ctrl-C para quitarlas.", len(buses))
	<-ctx.Done()
	log.Printf("cerrando turnos de las %d unidades de demo…", len(buses))
}

// doRetry is do() that treats 429 as "wait and retry" and a 409 conflict (driver already exists)
// as success — so a paced demo setup survives the rate limits instead of aborting.
func (c *client) doRetry(ctx context.Context, method, path, token string, body, out any) error {
	wait := 8 * time.Second
	for attempt := 0; attempt < 12; attempt++ {
		code, err := c.do(ctx, method, path, token, body, out, false)
		switch {
		case code == http.StatusConflict:
			return nil
		case code == http.StatusTooManyRequests:
			if !sleep(ctx, wait) {
				return ctx.Err()
			}
			wait = min(wait*2, time.Minute)
			continue
		default:
			return err
		}
	}
	return fmt.Errorf("%s %s: agotados los reintentos (429)", method, path)
}

// run moves the bus along its route, reports like the app and acks ramp requests. With socket
// false (demo), it only reports positions — no driver socket, so a single IP doesn't trip the
// socket rate limit while the whole fleet connects.
func (b *bus) run(ctx context.Context, socket bool) {
	if socket {
		go b.listen(ctx)
	}
	// Real phones aren't in step: spread the reports over the interval.
	if !sleep(ctx, rand.N(*gpsEvery)) {
		return
	}
	t := time.NewTicker(*gpsEvery)
	defer t.Stop()
	pos := rand.Float64()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pos += 0.002
		lon, lat := along(b.route.line, math.Mod(pos, 1))
		lat += rand.Float64() * 1e-7 // unique per report, so its arrival can be matched
		b.mu.Lock()
		b.sent[lat] = time.Now()
		for k, at := range b.sent {
			if time.Since(at) > time.Minute {
				delete(b.sent, k)
			}
		}
		b.mu.Unlock()
		m.gpsSent.Add(1)
		if _, err := b.c.do(ctx, "POST", "/api/v1/driver/positions", b.token,
			map[string]float64{"lat": lat, "lon": lon, "speedKmh": 25}, nil, true); err != nil && ctx.Err() == nil {
			m.gpsErr.Add(1)
		}
	}
}

func along(line [][2]float64, f float64) (lon, lat float64) {
	x := f * float64(len(line)-1)
	i := min(int(x), len(line)-2)
	t := x - float64(i)
	a, z := line[i], line[i+1]
	return a[0] + (z[0]-a[0])*t, a[1] + (z[1]-a[1])*t
}

func (b *bus) listen(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := b.c.dial(ctx, "/api/v1/driver/socket", b.token)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				break
			}
			var msg struct {
				RampRequests []struct{ ID uint64 } `json:"rampRequests"`
			}
			if json.Unmarshal(data, &msg) == nil {
				for _, r := range msg.RampRequests {
					go func(id uint64) {
						time.Sleep(time.Duration(2+rand.IntN(8)) * time.Second) // the driver taps "visto"
						b.c.do(ctx, "POST", fmt.Sprintf("/api/v1/driver/ramp-requests/%d/ack", id), b.token, nil, nil, false)
					}(r.ID)
				}
			}
		}
		conn.CloseNow()
	}
}

// ---- riders ----

type feedMsg struct {
	Positions []struct {
		DeviceID uint64  `json:"deviceId"`
		Latitude float64 `json:"latitude"`
	} `json:"positions"`
}

// rider is one phone with the app open: routes, live map, now and then a ramp request. When the
// session ends another rider takes its place, so the number of simultaneous riders stays constant.
func rider(ctx context.Context, cat *catalog, n int) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		c := newClient()
		etag := ""
		if rand.IntN(2) == 0 {
			etag = cat.etag // returning rider
		}
		c.fetchRoutes(ctx, etag)

		m.wsTry.Add(1)
		start := time.Now()
		conn, err := c.dial(ctx, "/api/socket", "")
		if err != nil {
			if ctx.Err() == nil {
				m.wsErr.Add(1)
				m.reason("socket: " + shortErr(err))
			}
			c.close()
			sleep(ctx, backoff) // the app backs off 2 s → 30 s
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = 2 * time.Second
		m.connect.add(time.Since(start))
		m.active.Add(1)

		sctx, cancel := context.WithCancel(ctx)
		if *session > 0 {
			d := time.Duration(rand.ExpFloat64() * float64(*session))
			time.AfterFunc(d, cancel)
		}
		if *rampRate > 0 && len(cat.accessible) > 0 {
			go c.rampRequests(sctx, cat)
		}
		err = readFeed(sctx, conn)
		m.active.Add(-1)
		if sctx.Err() == nil { // neither the session nor the test ended it: the server dropped us
			m.drops.Add(1)
			m.reason("caída: " + closeReason(err))
		}
		cancel()
		conn.CloseNow()
		c.close()
	}
}

func (c *client) fetchRoutes(ctx context.Context, etag string) {
	req, _ := http.NewRequestWithContext(ctx, "GET", *baseURL+"/api/v1/routes", nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if c.ip != "" {
		req.Header.Set("X-Forwarded-For", c.ip)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if ctx.Err() != nil {
		return
	}
	m.httpReq.Add(1)
	if err != nil {
		m.httpErr.Add(1)
		m.reason("http: " + shortErr(err))
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	m.http.add(time.Since(start))
	if resp.StatusCode >= 400 {
		m.httpErr.Add(1)
		m.reason(fmt.Sprintf("GET /api/v1/routes → %d", resp.StatusCode))
	}
}

func readFeed(ctx context.Context, conn *websocket.Conn) error {
	conn.SetReadLimit(1 << 20)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		now := time.Now()
		m.msgs.Add(1)
		m.bytes.Add(int64(len(data)))
		var msg feedMsg
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		for _, p := range msg.Positions {
			v, ok := busesByID.Load(p.DeviceID)
			if !ok {
				continue
			}
			b := v.(*bus)
			b.mu.Lock()
			at, ok := b.sent[p.Latitude]
			b.mu.Unlock()
			if ok {
				m.delivery.add(now.Sub(at))
			}
		}
	}
}

func closeReason(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		var ce websocket.CloseError
		if errors.As(err, &ce) && ce.Reason != "" {
			return ce.Reason
		}
		return s.String()
	}
	return shortErr(err)
}

// rampRequests: Poisson arrivals at -ramp-rate per minute; each request is polled every 3 s
// until a driver acknowledges it (or 30 s pass), as the app does.
func (c *client) rampRequests(ctx context.Context, cat *catalog) {
	for {
		wait := time.Duration(rand.ExpFloat64() / *rampRate * float64(time.Minute))
		if !sleep(ctx, wait) {
			return
		}
		s := cat.accessible[rand.IntN(len(cat.accessible))]
		var resp struct{ ID uint64 }
		if _, err := c.do(ctx, "POST", "/api/v1/ramp-requests", "", map[string]string{"routeId": s.route, "stopId": s.stop}, &resp, true); err != nil {
			continue
		}
		for i := 0; i < 10; i++ {
			if !sleep(ctx, 3*time.Second) {
				return
			}
			var st struct{ Status string }
			if _, err := c.do(ctx, "GET", fmt.Sprintf("/api/v1/ramp-requests/%d", resp.ID), "", nil, &st, true); err != nil || st.Status != "pending" {
				break
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

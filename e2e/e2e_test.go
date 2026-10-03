//go:build e2e

// End-to-end test against a running stack:
//
//	docker compose up -d
//	echo 'clave-de-prueba' | docker compose exec -T api /api driver-login CH-014 chofer.e2e@rutacree.mx
//	E2E_ADMIN_PASSWORD="$(cat secrets/admin_password)" E2E_DRIVER_EMAIL=chofer.e2e@rutacree.mx \
//	  E2E_DRIVER_PASSWORD=clave-de-prueba go test -tags e2e ./e2e -v
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var base = envOr("E2E_BASE", "http://localhost:8080")

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func call(t *testing.T, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func dial(t *testing.T, path, token string) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+path, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// readUntil reads socket messages until one contains key, or times out.
func readUntil(t *testing.T, c *websocket.Conn, key string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("esperando %q: %v", key, err)
		}
		m := map[string]any{}
		_ = json.Unmarshal(b, &m)
		if _, ok := m[key]; ok {
			return m
		}
	}
}

func TestFlow(t *testing.T) {
	// Routes are served in the app's GeoJSON format.
	resp, err := http.Get(base + "/api/v1/routes")
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	resp.Body.Close()
	req, _ := http.NewRequest("GET", base+"/api/v1/routes", nil)
	req.Header.Set("If-None-Match", etag)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("ETag: esperaba 304, llegó %d", resp.StatusCode)
	}

	// One login for everybody: a wrong password is rejected, the right one answers with the role.
	driverEmail := envOr("E2E_DRIVER_EMAIL", "chofer.e2e@rutacree.mx")
	if st, _ := call(t, "POST", "/api/v1/auth/login", "", map[string]any{
		"email": driverEmail, "password": "incorrecta-123"}); st != 401 {
		t.Fatalf("contraseña incorrecta: esperaba 401, llegó %d", st)
	}
	st, signIn := call(t, "POST", "/api/v1/auth/login", "", map[string]any{
		"email": driverEmail, "password": os.Getenv("E2E_DRIVER_PASSWORD")})
	if st != 200 || signIn["role"] != "driver" {
		t.Fatalf("login de chofer: %d %v", st, signIn)
	}

	// Driver opens a shift with their login token.
	st, shift := call(t, "POST", "/api/v1/driver/shifts", signIn["token"].(string), map[string]any{
		"busNumber": "245", "routeId": "ruta-2"})
	if st != 201 {
		t.Fatalf("iniciar turno: %d %v", st, shift)
	}
	token := shift["token"].(string)
	busID := shift["busId"].(float64)

	// Rider connects to the public socket, driver to theirs.
	rider := dial(t, "/api/socket", "")
	defer rider.CloseNow()
	driver := dial(t, "/api/v1/driver/socket", token)
	defer driver.CloseNow()

	// Position via the OsmAnd endpoint the app already speaks (speed in knots).
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/driver/osmand?id=245&lat=24.0277&lon=-104.6700&speed=10", base), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("osmand: %d", resp.StatusCode)
	}
	m := readUntil(t, rider, "positions")
	pos := m["positions"].([]any)[0].(map[string]any)
	if pos["deviceId"].(float64) != busID || pos["latitude"].(float64) != 24.0277 {
		t.Errorf("posición inesperada: %v", pos)
	}
	dev := m["devices"].([]any)[0].(map[string]any)
	if dev["attributes"].(map[string]any)["ruta"] != "ruta-2" {
		t.Errorf("device sin ruta: %v", dev)
	}

	// The driver's console sees the position arrive even without Traccar Client (the app's GPS).
	if st, tr := call(t, "GET", "/api/v1/driver/tracker", token, nil); st != 200 || tr["busFixAgeSeconds"] == nil || tr["busFixAgeSeconds"].(float64) > 5 {
		t.Errorf("estado del rastreo sin la ubicación de la unidad: %d %v", st, tr)
	}

	// Another bus's id is refused.
	req, _ = http.NewRequest("GET", base+"/api/v1/driver/osmand?id=999&lat=24.02&lon=-104.67", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("osmand con otra unidad: esperaba 403, llegó %d", resp.StatusCode)
	}

	// Ramp request reaches the driver on that route; the driver acknowledges it.
	st, rr := call(t, "POST", "/api/v1/ramp-requests", "", map[string]any{"routeId": "ruta-2", "stopId": "ruta-2-p1"})
	if st != 201 || rr["driversNotified"].(float64) != 1 {
		t.Fatalf("solicitud de rampa: %d %v", st, rr)
	}
	got := readUntil(t, driver, "rampRequests")["rampRequests"].([]any)[0].(map[string]any)
	if got["stopName"] != "Terminal Poniente" {
		t.Errorf("rampa: %v", got)
	}
	if st, _ := call(t, "POST", fmt.Sprintf("/api/v1/driver/ramp-requests/%d/ack", int(rr["id"].(float64))), token, nil); st != 204 {
		t.Errorf("ack: %d", st)
	}
	if _, s := call(t, "GET", fmt.Sprintf("/api/v1/ramp-requests/%d", int(rr["id"].(float64))), "", nil); s["status"] != "acknowledged" {
		t.Errorf("estado rampa: %v", s)
	}

	// Rider report.
	if st, _ := call(t, "POST", "/api/v1/reports", "", map[string]any{"type": "bus_full", "routeId": "ruta-2", "comment": "Lleno"}); st != 201 {
		t.Errorf("reporte: %d", st)
	}

	// Panic: riders get the alarm, the shift closes and the token dies with it.
	if st, _ := call(t, "POST", "/api/v1/driver/incidents", token, map[string]any{"type": "breakdown"}); st != 201 {
		t.Fatalf("incidente: %d", st)
	}
	ev := readUntil(t, rider, "events")["events"].([]any)[0].(map[string]any)
	if ev["type"] != "alarm" || ev["attributes"].(map[string]any)["alarm"] != "fault" {
		t.Errorf("evento: %v", ev)
	}
	hidden := readUntil(t, rider, "devices")["devices"].([]any)[0].(map[string]any)
	if _, has := hidden["attributes"].(map[string]any)["ruta"]; has {
		t.Errorf("la unidad debería ocultarse: %v", hidden)
	}
	if st, _ := call(t, "POST", "/api/v1/driver/positions", token, map[string]any{"lat": 24.03, "lon": -104.67, "speedKmh": 20}); st != 401 {
		t.Errorf("token tras cerrar turno: esperaba 401, llegó %d", st)
	}

	// Admin dashboard.
	pass := os.Getenv("E2E_ADMIN_PASSWORD")
	if pass == "" {
		t.Skip("define E2E_ADMIN_PASSWORD para probar el panel")
	}
	st, login := call(t, "POST", "/api/v1/admin/login", "", map[string]any{"email": envOr("E2E_ADMIN_EMAIL", "admin@rutacree.mx"), "password": pass})
	if st != 200 {
		t.Fatalf("login admin: %d", st)
	}
	admin := login["token"].(string)
	if st, _ := call(t, "GET", "/api/v1/admin/overview", token, nil); st != 401 && st != 403 {
		t.Errorf("token de chofer en panel: esperaba 401/403, llegó %d", st)
	}
	st, ov := call(t, "GET", "/api/v1/admin/overview", admin, nil)
	if st != 200 || ov["openIncidents"].(float64) < 1 {
		t.Errorf("overview: %d %v", st, ov)
	}
}

func TestSecurity(t *testing.T) {
	// alg=none token.
	none := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJyb2xlIjoiYWRtaW4iLCJzdWIiOiIxIiwiZXhwIjo0MTAyNDQ0ODAwfQ."
	if st, _ := call(t, "GET", "/api/v1/admin/overview", none, nil); st != 401 {
		t.Errorf("alg=none: esperaba 401, llegó %d", st)
	}
	// Unknown JSON fields and wrong content type are refused.
	if st, _ := call(t, "POST", "/api/v1/reports", "", map[string]any{"type": "bus_full", "admin": true}); st != 400 {
		t.Errorf("campo desconocido: esperaba 400, llegó %d", st)
	}
	resp, _ := http.Post(base+"/api/v1/reports", "text/plain", strings.NewReader(`{"type":"bus_full"}`))
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Errorf("content-type: esperaba 415, llegó %d", resp.StatusCode)
	}
	// Security headers.
	resp, _ = http.Get(base + "/healthz")
	resp.Body.Close()
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("falta X-Content-Type-Options")
	}
	// Browser from a foreign origin can't open the socket.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, r, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/api/socket",
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"https://evil.example"}}})
	if err == nil || r == nil || r.StatusCode != 403 {
		t.Errorf("origen ajeno debería recibir 403")
	}
}

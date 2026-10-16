package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eter/rutacree-backend/internal/httpx"
)

// Router asks an OSRM server for the street path between the points the route editor clicks, and
// for the name of the street under a stop. The panel can't call OSRM itself (its CSP only allows
// this origin), and going through here also caches answers and keeps the editor working the same
// if OSRM is swapped for a self-hosted one (ROUTING_URL).
type Router struct {
	base   string
	client *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at   time.Time
	body []byte
}

const (
	routeCacheTTL  = 24 * time.Hour
	routeCacheSize = 2000
	maxRoutePoints = 25
)

func NewRouter(base string) *Router {
	return &Router{base: base, client: &http.Client{Timeout: 12 * time.Second}, cache: map[string]cached{}}
}

// RoutePath is GET /api/v1/admin/routing/route?points=lon,lat;lon,lat[;…]: the street path through
// the points in order, as {"coordinates": [[lon,lat],…], "distance": m, "duration": s, "legs": [{"distance","duration"}]}.
func (h *Handler) RoutePath(w http.ResponseWriter, r *http.Request) error {
	pts, err := parsePoints(r.URL.Query().Get("points"), 2, maxRoutePoints)
	if err != nil {
		return err
	}
	q := url.Values{"overview": {"full"}, "geometries": {"geojson"}, "steps": {"false"}, "continue_straight": {"true"}}
	body, err := h.Routing.get(r.Context(), "/route/v1/driving/"+pts+"?"+q.Encode())
	if err != nil {
		return err
	}
	var res struct {
		Code   string `json:"code"`
		Routes []struct {
			Distance float64 `json:"distance"`
			Duration float64 `json:"duration"`
			Geometry struct {
				Coordinates [][]float64 `json:"coordinates"`
			} `json:"geometry"`
			Legs []struct {
				Distance float64 `json:"distance"`
				Duration float64 `json:"duration"`
			} `json:"legs"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Code != "Ok" || len(res.Routes) == 0 {
		return httpx.NewError(http.StatusUnprocessableEntity, "no_route", "No se encontró camino por las calles entre esos puntos")
	}
	rt := res.Routes[0]
	for _, c := range rt.Geometry.Coordinates {
		c[0], c[1] = round6(c[0]), round6(c[1])
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"coordinates": rt.Geometry.Coordinates, "distance": rt.Distance, "duration": rt.Duration, "legs": rt.Legs,
	})
	return nil
}

// StreetName is GET /api/v1/admin/routing/street?point=lon,lat: the nearest street's name (to name
// a new stop) and the point snapped onto it: {"name": "Boulevard Durango", "lon", "lat"}.
func (h *Handler) StreetName(w http.ResponseWriter, r *http.Request) error {
	pt, err := parsePoints(r.URL.Query().Get("point"), 1, 1)
	if err != nil {
		return err
	}
	body, err := h.Routing.get(r.Context(), "/nearest/v1/driving/"+pt+"?number=3")
	if err != nil {
		return err
	}
	var res struct {
		Code      string `json:"code"`
		Waypoints []struct {
			Name     string    `json:"name"`
			Location []float64 `json:"location"`
		} `json:"waypoints"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Code != "Ok" || len(res.Waypoints) == 0 {
		return httpx.NewError(http.StatusUnprocessableEntity, "no_street", "No hay calles cerca de ese punto")
	}
	// The nearest piece of road may be unnamed (an access lane); the next ones usually aren't.
	out := map[string]any{"name": "", "lon": round6(res.Waypoints[0].Location[0]), "lat": round6(res.Waypoints[0].Location[1])}
	var names []string
	for _, wp := range res.Waypoints {
		if n := strings.TrimSpace(wp.Name); n != "" && !contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) > 0 {
		out["name"] = names[0]
	}
	if len(names) > 1 {
		out["cross"] = names[1]
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// parsePoints validates "lon,lat;lon,lat" (Mexico's bounding box, generously) and re-formats it.
func parsePoints(s string, min, max int) (string, error) {
	parts := strings.Split(s, ";")
	if s == "" || len(parts) < min || len(parts) > max {
		return "", httpx.BadRequest(fmt.Sprintf("Se esperan de %d a %d puntos lon,lat separados por ;", min, max))
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		ll := strings.Split(p, ",")
		if len(ll) != 2 {
			return "", httpx.BadRequest("Punto inválido: " + p)
		}
		lon, err1 := strconv.ParseFloat(strings.TrimSpace(ll[0]), 64)
		lat, err2 := strconv.ParseFloat(strings.TrimSpace(ll[1]), 64)
		if err1 != nil || err2 != nil || lon < -120 || lon > -85 || lat < 13 || lat > 34 {
			return "", httpx.BadRequest("Punto fuera de México: " + p)
		}
		out[i] = strconv.FormatFloat(lon, 'f', 6, 64) + "," + strconv.FormatFloat(lat, 'f', 6, 64)
	}
	return strings.Join(out, ";"), nil
}

func (rt *Router) get(ctx context.Context, path string) ([]byte, error) {
	if rt == nil || rt.base == "" {
		return nil, httpx.NewError(http.StatusServiceUnavailable, "routing_off", "El ajuste a calles no está configurado (ROUTING_URL)")
	}
	rt.mu.Lock()
	if c, ok := rt.cache[path]; ok && time.Since(c.at) < routeCacheTTL {
		rt.mu.Unlock()
		return c.body, nil
	}
	rt.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rt.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RutaCree-panel (editor de rutas)")
	res, err := rt.client.Do(req)
	if err != nil {
		return nil, httpx.NewError(http.StatusBadGateway, "routing_down", "El servicio de calles no responde; intenta de nuevo o usa trazo libre")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// OSRM answers 400 with a JSON code (NoRoute, NoSegment): let the caller turn it into a message.
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		return nil, httpx.NewError(http.StatusBadGateway, "routing_down", "El servicio de calles está ocupado; intenta en unos segundos o usa trazo libre")
	}
	if res.StatusCode == http.StatusOK {
		rt.mu.Lock()
		if len(rt.cache) >= routeCacheSize {
			rt.cache = map[string]cached{}
		}
		rt.cache[path] = cached{time.Now(), body}
		rt.mu.Unlock()
	}
	return body, nil
}

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutingProxy(t *testing.T) {
	calls := 0
	osrm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch {
		case strings.HasPrefix(r.URL.Path, "/route/v1/driving/-104.654300,24.045000;-104.650000,24.040000"):
			w.Write([]byte(`{"code":"Ok","routes":[{"distance":812.4,"duration":95.1,"geometry":{"coordinates":[[-104.6543001,24.045],[-104.65,24.04]]},"legs":[{"distance":812.4,"duration":95.1}]}]}`))
		case strings.HasPrefix(r.URL.Path, "/nearest/"):
			w.Write([]byte(`{"code":"Ok","waypoints":[{"name":"","location":[-104.6,24.0]},{"name":"Boulevard Durango","location":[-104.6,24.0]},{"name":"Calle Bravo","location":[-104.6,24.0]}]}`))
		default:
			w.WriteHeader(400)
			w.Write([]byte(`{"code":"NoRoute"}`))
		}
	}))
	defer osrm.Close()
	h := &Handler{Routing: NewRouter(osrm.URL)}

	for i := 0; i < 2; i++ { // second call comes from the cache
		rec := httptest.NewRecorder()
		if err := h.RoutePath(rec, httptest.NewRequest("GET", "/x?points=-104.6543,24.045%3B-104.65,24.04", nil)); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Coordinates [][]float64 `json:"coordinates"`
			Distance    float64     `json:"distance"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		if len(got.Coordinates) != 2 || got.Distance != 812.4 || got.Coordinates[0][0] != -104.6543 {
			t.Fatalf("route: %s", rec.Body)
		}
	}
	if calls != 1 {
		t.Errorf("cache: %d calls to OSRM", calls)
	}
	if err := h.RoutePath(httptest.NewRecorder(), httptest.NewRequest("GET", "/x?points=-104.1,24.1%3B-104.2,24.2", nil)); err == nil {
		t.Error("NoRoute should be an error")
	}
	if err := h.RoutePath(httptest.NewRecorder(), httptest.NewRequest("GET", "/x?points=2.35,48.85%3B-104.2,24.2", nil)); err == nil {
		t.Error("Paris should be rejected")
	}
	rec := httptest.NewRecorder()
	if err := h.StreetName(rec, httptest.NewRequest("GET", "/x?point=-104.6,24.0", nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), `"name":"Boulevard Durango"`) || !strings.Contains(rec.Body.String(), `"cross":"Calle Bravo"`) {
		t.Errorf("street: %s", rec.Body)
	}
}

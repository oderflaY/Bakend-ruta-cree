package traccar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/eter/rutacree-backend/internal/live"
)

// fakeTraccar serves the bits of Traccar's API the bridge uses and pushes `positions` on the socket.
func fakeTraccar(t *testing.T, positions []Position) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var registered atomic.Int32
	users := map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if pw, ok := users[r.Form.Get("email")]; !ok || pw != r.Form.Get("password") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "abc", Path: "/"})
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		var u struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&u)
		users[u.Email] = u.Password
		registered.Add(1)
		_, _ = w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /api/server", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"registration":false}`))
	})
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]Device{{ID: 7, UniqueID: "245"}, {ID: 8, UniqueID: "999"}})
	})
	mux.HandleFunc("GET /api/socket", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("JSESSIONID"); err != nil || c.Value != "abc" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		b, _ := json.Marshal(map[string]any{"positions": positions})
		_ = conn.Write(r.Context(), websocket.MessageText, b)
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &registered
}

func TestBridgeFeedsOnlyFreshValidFixesOfBusesOnShift(t *testing.T) {
	now := time.Now().UTC()
	srv, registered := fakeTraccar(t, []Position{
		{DeviceID: 7, Latitude: 24.02, Longitude: -104.66, Speed: 10, Valid: true, FixTime: now},   // bus 245 on shift
		{DeviceID: 8, Latitude: 1, Longitude: 1, Valid: true, FixTime: now},                        // bus 999: no shift
		{DeviceID: 7, Latitude: 9, Longitude: 9, Valid: false, FixTime: now.Add(time.Second)},      // no GPS fix
		{DeviceID: 7, Latitude: 8, Longitude: 8, Valid: true, FixTime: now.Add(-10 * time.Minute)}, // offline backlog
		{DeviceID: 42, Latitude: 7, Longitude: 7, Valid: true, FixTime: now},                       // unknown device
	})

	hub := live.NewHub(10)
	hub.AddShift(live.Shift{ID: 1, BusID: 100, BusNumber: "245", RouteID: "ruta-2", Ramp: true})
	client, err := NewClient(srv.URL, "api@rutacree.mx", "una-clave-larga-segura")
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewBridge(client, hub, live.NewRecorder(nil), "http://host:5055")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bridge.Run(ctx)

	deadline := time.After(3 * time.Second)
	for {
		if _, ok := bridge.LastFix("245"); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the bridge never delivered the fix of bus 245")
		case <-time.After(20 * time.Millisecond):
		}
	}

	if registered.Load() != 1 {
		t.Errorf("service account registrations = %d, want 1 (first run on an empty Traccar)", registered.Load())
	}
	lat, lon, ok := hub.LastFix(1)
	if !ok || lat != 24.02 || lon != -104.66 {
		t.Errorf("hub position = %v,%v (ok=%v); invalid, stale or foreign fixes must not overwrite it", lat, lon, ok)
	}
	if shifts := hub.ActiveShifts(); len(shifts) != 1 || shifts[0].SpeedKmh == nil || *shifts[0].SpeedKmh < 18.5 || *shifts[0].SpeedKmh > 18.6 {
		t.Errorf("speed must be converted from knots to km/h (10 kn = 18.52 km/h): %+v", shifts)
	}
	if _, ok := bridge.LastFix("999"); ok {
		t.Error("a bus without an open shift must be ignored")
	}
	if !bridge.Connected() {
		t.Error("Connected() should be true while the stream is open")
	}
}

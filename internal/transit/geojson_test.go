package transit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The app's own route files must import unchanged.
func TestParseAppRoutes(t *testing.T) {
	files, _ := filepath.Glob("../../seed/routes/*.geojson")
	if len(files) == 0 {
		t.Fatal("no hay rutas en seed/routes")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		r, err := ParseRoute(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(r.Stops) == 0 || !strings.HasPrefix(r.Stops[0].ID, r.ID) {
			t.Errorf("%s: paradas inesperadas %+v", f, r.Stops)
		}
	}
}

func TestParseRouteRejects(t *testing.T) {
	line := `{"type":"Feature","properties":{"id":"r-1","name":"R"},"geometry":{"type":"LineString","coordinates":[[-104.6,24.0],[-104.7,24.1]]}}`
	cases := map[string]string{
		"no collection":  `{"type":"Feature"}`,
		"no line":        `{"type":"FeatureCollection","features":[]}`,
		"bad id":         strings.Replace(`{"type":"FeatureCollection","features":[LINE]}`, "LINE", strings.Replace(line, "r-1", "R 1; DROP", 1), 1),
		"bad coordinate": strings.Replace(`{"type":"FeatureCollection","features":[LINE]}`, "LINE", strings.Replace(line, "24.1", "240.1", 1), 1),
		"bad schedule": strings.Replace(`{"type":"FeatureCollection","features":[LINE,{"type":"Feature","properties":{"name":"P","horarios":["25:00"]},"geometry":{"type":"Point","coordinates":[-104.6,24.0]}}]}`,
			"LINE", line, 1),
	}
	for name, in := range cases {
		if _, err := ParseRoute([]byte(in)); err == nil {
			t.Errorf("%s: debería rechazarse", name)
		}
	}
}

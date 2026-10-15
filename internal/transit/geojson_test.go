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

// Municipios and the start of the return trip are filled in for the published routes.
func TestRouteMunicipiosAndReturn(t *testing.T) {
	want := map[string][]string{
		"azul-3": {"Durango"},
		"azul-4": {"Durango"},
		"cas-1":  {"Lerdo", "Gómez Palacio"},
		"cas-3":  {"Lerdo", "Gómez Palacio"},
	}
	for id, municipios := range want {
		data, err := os.ReadFile("../../seed/routes/" + id + ".geojson")
		if err != nil {
			t.Fatal(err)
		}
		r, err := ParseRoute(data)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(r.Municipios, ",") != strings.Join(municipios, ",") {
			t.Errorf("%s: municipios %v, se esperaba %v", id, r.Municipios, municipios)
		}
		if !strings.Contains(string(r.GeoJSON), `"regreso_desde":`) {
			t.Errorf("%s: sin regreso_desde", id)
		}
	}
	// A one-way line has no return; an unknown municipio is rejected.
	oneWay := `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"id":"r-1","name":"R"},"geometry":{"type":"LineString","coordinates":[[-104.6,24.0],[-104.7,24.1]]}}]}`
	r, err := ParseRoute([]byte(oneWay))
	if err != nil || strings.Contains(string(r.GeoJSON), "regreso_desde") || r.Municipios[0] != "Durango" {
		t.Errorf("ruta de un sentido: %v %s", err, r.GeoJSON)
	}
	if _, err := ParseRoute([]byte(strings.Replace(oneWay, `"name":"R"`, `"name":"R","municipios":["Atlantis"]`, 1))); err == nil {
		t.Error("municipio desconocido debería rechazarse")
	}
}

// Package transit owns the route catalog (routes + stops) the app draws on the map.
package transit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	idPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	clockPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
)

type Stop struct {
	ID         string
	Name       string
	Lat, Lon   float64
	Accessible bool
	Seq        int
}

type Route struct {
	ID    string
	Name  string
	Color string
	Stops []Stop
	// Municipios the route serves, so apps and panel can show only the local ones.
	Municipios []string
	// GeoJSON as the app's parseRouteGeoJson expects it, re-encoded compactly.
	GeoJSON json.RawMessage
}

type feature struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

// ParseRoute validates a FeatureCollection with one LineString (the route: id, name, color) and
// Point features for the stops (name, accessible, horarios). Stops without an id get
// "<route>-p<n>" written back into the GeoJSON, so app and backend always agree on stop ids.
func ParseRoute(data []byte) (*Route, error) {
	var fc struct {
		Type     string    `json:"type"`
		Features []feature `json:"features"`
	}
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("GeoJSON inválido: %w", err)
	}
	if fc.Type != "FeatureCollection" {
		return nil, fmt.Errorf("se espera un FeatureCollection")
	}
	if len(fc.Features) > 2000 {
		return nil, fmt.Errorf("demasiadas features")
	}

	r := &Route{}
	lines := 0
	var line *feature
	var lineCoords [][]float64
	for i := range fc.Features {
		f := &fc.Features[i]
		if f.Properties == nil {
			f.Properties = map[string]any{}
		}
		switch f.Geometry.Type {
		case "LineString":
			lines++
			line = f
			var coords [][]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &coords); err != nil || len(coords) < 2 || len(coords) > 20000 {
				return nil, fmt.Errorf("LineString inválido")
			}
			for _, c := range coords {
				if err := checkCoord(c); err != nil {
					return nil, err
				}
			}
			lineCoords = coords
			r.ID, _ = f.Properties["id"].(string)
			r.Name, _ = f.Properties["name"].(string)
			r.Color, _ = f.Properties["color"].(string)
		case "Point":
			// handled below, once the route id is known
		default:
			return nil, fmt.Errorf("geometría no soportada: %q", f.Geometry.Type)
		}
	}
	if lines != 1 {
		return nil, fmt.Errorf("se espera exactamente un LineString (la ruta), hay %d", lines)
	}
	if !idPattern.MatchString(r.ID) {
		return nil, fmt.Errorf("id de ruta inválido (minúsculas, números y guiones): %q", r.ID)
	}
	if r.Name = strings.TrimSpace(r.Name); r.Name == "" || len(r.Name) > 120 {
		return nil, fmt.Errorf("nombre de ruta vacío o muy largo")
	}
	if r.Color == "" {
		r.Color = "#8ECAE6"
	}
	if !colorPattern.MatchString(r.Color) {
		return nil, fmt.Errorf("color inválido: %q", r.Color)
	}

	seen := map[string]bool{}
	for i := range fc.Features {
		f := &fc.Features[i]
		if f.Geometry.Type != "Point" {
			continue
		}
		var c []float64
		if err := json.Unmarshal(f.Geometry.Coordinates, &c); err != nil {
			return nil, fmt.Errorf("Point inválido")
		}
		if err := checkCoord(c); err != nil {
			return nil, err
		}
		s := Stop{Lon: c[0], Lat: c[1], Seq: len(r.Stops)}
		s.ID, _ = f.Properties["id"].(string)
		if s.ID == "" {
			s.ID = fmt.Sprintf("%s-p%d", r.ID, len(r.Stops)+1)
			f.Properties["id"] = s.ID
		}
		if !idPattern.MatchString(s.ID) || seen[s.ID] {
			return nil, fmt.Errorf("id de parada inválido o repetido: %q", s.ID)
		}
		seen[s.ID] = true
		s.Name, _ = f.Properties["name"].(string)
		if s.Name = strings.TrimSpace(s.Name); s.Name == "" || len(s.Name) > 120 {
			return nil, fmt.Errorf("parada %s sin nombre o nombre muy largo", s.ID)
		}
		s.Accessible, _ = f.Properties["accessible"].(bool)
		if h, ok := f.Properties["horarios"]; ok {
			list, ok := h.([]any)
			if !ok || len(list) > 500 {
				return nil, fmt.Errorf("horarios inválidos en %s", s.ID)
			}
			for _, v := range list {
				if str, ok := v.(string); !ok || !clockPattern.MatchString(str) {
					return nil, fmt.Errorf("horario inválido en %s: %v (formato HH:MM)", s.ID, v)
				}
			}
		}
		r.Stops = append(r.Stops, s)
	}

	// Written back so the apps and the panel read them instead of each inferring them again.
	var err error
	if r.Municipios, err = municipiosOf(line.Properties, lineCoords, r.Stops); err != nil {
		return nil, err
	}
	line.Properties["municipios"] = r.Municipios
	split, err := regresoDesde(line.Properties, lineCoords, r.Stops)
	if err != nil {
		return nil, err
	}
	if split > 0 {
		line.Properties["regreso_desde"] = split
	} else {
		delete(line.Properties, "regreso_desde")
	}

	out, err := json.Marshal(map[string]any{"type": "FeatureCollection", "features": fc.Features})
	if err != nil {
		return nil, err
	}
	r.GeoJSON = out
	return r, nil
}

func checkCoord(c []float64) error {
	if len(c) < 2 || c[0] < -180 || c[0] > 180 || c[1] < -90 || c[1] > 90 {
		return fmt.Errorf("coordenada fuera de rango: %v", c)
	}
	return nil
}

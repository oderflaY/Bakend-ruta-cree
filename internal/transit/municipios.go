package transit

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Cabeceras de los municipios de Durango con servicio posible (mismas que el selector del panel,
// frontend-admin-react/src/municipios.ts). Una ruta pertenece al municipio cuya cabecera queda más
// cerca de cada una de sus paradas.
var cabeceras = []struct {
	Name     string
	Lat, Lon float64
}{
	{"Durango", 24.0277, -104.6532},
	{"Gómez Palacio", 25.5611, -103.4983},
	{"Lerdo", 25.5383, -103.5245},
	{"Santiago Papasquiaro", 25.0439, -105.4192},
	{"Pueblo Nuevo (El Salto)", 23.7783, -105.3606},
	{"Canatlán", 24.5250, -104.7740},
	{"Guadalupe Victoria", 24.4469, -104.1211},
	{"Nombre de Dios", 23.8492, -104.2461},
	{"Vicente Guerrero", 23.7353, -103.9842},
	{"Poanas (Villa Unión)", 23.9744, -104.0469},
	{"Mezquital", 23.4736, -104.3961},
	{"Súchil", 23.6211, -103.9233},
	{"Cuencamé", 24.8711, -103.6975},
	{"Peñón Blanco", 24.7881, -104.0306},
	{"Pánuco de Coronado", 24.5392, -104.3289},
	{"San Juan del Río", 24.7769, -104.4614},
	{"Rodeo", 25.1778, -104.5594},
	{"Nazas", 25.2250, -104.1131},
	{"Mapimí", 25.8333, -103.8500},
	{"Tlahualilo", 26.1053, -103.4436},
	{"Santa María del Oro", 25.9500, -105.3667},
	{"Tepehuanes", 25.3394, -105.7258},
	{"Guanaceví", 25.9333, -105.9500},
	{"Topia", 25.2119, -106.5711},
	{"Tamazula", 24.9686, -106.9672},
}

// Destinos de las rutas del DIF (CREE en Durango, CRIT en La Laguna): donde termina la ida.
var destinoPattern = regexp.MustCompile(`^(CREE|CRIT)\b`)

// A route whose ends are this close is a loop (sale y vuelve al mismo punto).
const loopGapMeters = 500

func metersBetween(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := p2-p1, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

func nearestCabecera(lat, lon float64) string {
	best, bestD := "", math.Inf(1)
	for _, c := range cabeceras {
		if d := metersBetween(lat, lon, c.Lat, c.Lon); d < bestD {
			best, bestD = c.Name, d
		}
	}
	return best
}

// municipiosOf reads the route's "municipios" (a name or a list) or infers them from its stops
// (from the line when it has none). Names are matched to the catalog ignoring case.
func municipiosOf(props map[string]any, line [][]float64, stops []Stop) ([]string, error) {
	var given []string
	switch v := props["municipios"].(type) {
	case nil:
	case string:
		given = []string{v}
	case []any:
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				return nil, fmt.Errorf("municipios debe ser una lista de nombres")
			}
			given = append(given, s)
		}
	default:
		return nil, fmt.Errorf("municipios debe ser una lista de nombres")
	}
	out := []string{}
	add := func(name string) {
		for _, x := range out {
			if x == name {
				return
			}
		}
		out = append(out, name)
	}
	for _, g := range given {
		found := ""
		for _, c := range cabeceras {
			if strings.EqualFold(strings.TrimSpace(g), c.Name) {
				found = c.Name
			}
		}
		if found == "" {
			return nil, fmt.Errorf("municipio desconocido: %q", g)
		}
		add(found)
	}
	if len(out) > 0 {
		return out, nil
	}
	if len(stops) > 0 {
		for _, s := range stops {
			add(nearestCabecera(s.Lat, s.Lon))
		}
	} else {
		for _, c := range line {
			add(nearestCabecera(c[1], c[0]))
		}
	}
	return out, nil
}

// regresoDesde is the index of the line vertex where the return trip starts: the vertex nearest
// the destination stop ("destino": stop id or name; else a stop named CREE/CRIT). Only loops have
// a return; -1 means the whole line is one direction.
func regresoDesde(props map[string]any, line [][]float64, stops []Stop) (int, error) {
	var dest *Stop
	if want, ok := props["destino"].(string); ok && want != "" {
		for i := range stops {
			if stops[i].ID == want || strings.EqualFold(stops[i].Name, want) {
				dest = &stops[i]
				break
			}
		}
		if dest == nil {
			return 0, fmt.Errorf("destino %q no es una parada de la ruta", want)
		}
	}
	first, last := line[0], line[len(line)-1]
	if metersBetween(first[1], first[0], last[1], last[0]) > loopGapMeters || len(line) < 3 {
		return -1, nil
	}
	if dest == nil {
		for i := range stops {
			if destinoPattern.MatchString(stops[i].Name) {
				dest = &stops[i]
				break
			}
		}
	}
	best, bestD := -1, math.Inf(1)
	if dest != nil {
		for i := 1; i < len(line)-1; i++ {
			if d := metersBetween(line[i][1], line[i][0], dest.Lat, dest.Lon); d < bestD {
				best, bestD = i, d
			}
		}
		return best, nil
	}
	// Without a known destination the turnaround is the point farthest from the start.
	bestD = -1
	for i := 1; i < len(line)-1; i++ {
		if d := metersBetween(line[i][1], line[i][0], first[1], first[0]); d > bestD {
			best, bestD = i, d
		}
	}
	return best, nil
}

package formatos

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// Stats is what the drivers' reports say about a period: trips (vueltas), the users they counted
// when each trip started, by route, day, hour, weekday and driver, and the failures they reported.
// The panel draws it and the workbooks carry it as the ESTADÍSTICAS sheet.
type Stats struct {
	From string `json:"desde"` // YYYY-MM-DD, local, inclusive
	To   string `json:"hasta"`
	// Trips started in the period, and how many of them have the users counted (a trip started
	// before the counts were asked has none).
	Trips   int         `json:"vueltas"`
	Counted int         `json:"vueltasConConteo"`
	Users   Counts      `json:"usuarios"`
	Routes  []RouteStat `json:"rutas"`  // most users first
	Days    []DayStat   `json:"dias"`   // every day of the period
	Hours   []HourStat  `json:"horas"`  // 0–23, by departure time
	Week    []DayStat   `json:"semana"` // Monday to Sunday
	Drivers []NameStat  `json:"choferes"`
	// Failures reported by the drivers: accident, breakdown, ramp_failure.
	Incidents map[string]int `json:"incidentes"`
}

type RouteStat struct {
	ID    string `json:"id"`
	Name  string `json:"nombre"`
	Color string `json:"color"`
	Trips int    `json:"vueltas"`
	Users Counts `json:"usuarios"`
}

type DayStat struct {
	Day   string `json:"dia"` // YYYY-MM-DD, or the weekday's name in Week
	Trips int    `json:"vueltas"`
	Users Counts `json:"usuarios"`
}

type HourStat struct {
	Hour  int `json:"hora"`
	Trips int `json:"vueltas"`
	Users int `json:"usuarios"`
}

type NameStat struct {
	Name  string `json:"nombre"`
	Trips int    `json:"vueltas"`
	Users int    `json:"usuarios"`
}

// MarshalJSON writes the counts with their total.
func (c Counts) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]int{"hombres": c.Men, "mujeres": c.Women, "total": c.Total(),
		"discapacidad": c.Disability, "adultosMayores": c.Elderly, "embarazadas": c.Pregnant, "acompanantes": c.Companions})
}

// RouteInfo names a route for the statistics.
type RouteInfo struct{ Name, Color string }

// IncidentAt is a failure a driver reported.
type IncidentAt struct {
	Kind string
	At   time.Time
}

var weekNames = [7]string{"LUNES", "MARTES", "MIÉRCOLES", "JUEVES", "VIERNES", "SÁBADO", "DOMINGO"}

// Summarize computes the statistics of the trips started from `from` to `to` (local days, inclusive).
func Summarize(from, to time.Time, trips []Trip, incidents []IncidentAt, routes map[string]RouteInfo) Stats {
	s := Stats{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly), Incidents: map[string]int{}}
	days := map[string]int{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		days[d.Format(time.DateOnly)] = len(s.Days)
		s.Days = append(s.Days, DayStat{Day: d.Format(time.DateOnly)})
	}
	for h := 0; h < 24; h++ {
		s.Hours = append(s.Hours, HourStat{Hour: h})
	}
	for _, n := range weekNames {
		s.Week = append(s.Week, DayStat{Day: n})
	}
	byRoute := map[string]*RouteStat{}
	byDriver := map[string]*NameStat{}
	for _, t := range trips {
		i, ok := days[t.Started.Format(time.DateOnly)]
		if !ok {
			continue
		}
		s.Trips++
		if t.Counted {
			s.Counted++
		}
		s.Users.add(t.Users)
		s.Days[i].Trips++
		s.Days[i].Users.add(t.Users)
		h := &s.Hours[t.Started.Hour()]
		h.Trips++
		h.Users += t.Users.Total()
		wd := &s.Week[(int(t.Started.Weekday())+6)%7]
		wd.Trips++
		wd.Users.add(t.Users)

		r := byRoute[t.RouteID]
		if r == nil {
			info := routes[t.RouteID]
			if info.Name == "" {
				info.Name = t.RouteID
			}
			r = &RouteStat{ID: t.RouteID, Name: info.Name, Color: info.Color}
			byRoute[t.RouteID] = r
		}
		r.Trips++
		r.Users.add(t.Users)

		name := strings.ToUpper(strings.TrimSpace(t.Driver))
		if name == "" {
			name = "SIN NOMBRE"
		}
		d := byDriver[name]
		if d == nil {
			d = &NameStat{Name: name}
			byDriver[name] = d
		}
		d.Trips++
		d.Users += t.Users.Total()
	}
	// Every route of the catalog shows up, even with no trips: "rutas más usadas" ranks all of them.
	for id, info := range routes {
		if byRoute[id] == nil {
			byRoute[id] = &RouteStat{ID: id, Name: info.Name, Color: info.Color}
		}
	}
	for _, r := range byRoute {
		s.Routes = append(s.Routes, *r)
	}
	sort.Slice(s.Routes, func(i, j int) bool {
		a, b := s.Routes[i], s.Routes[j]
		if a.Users.Total() != b.Users.Total() {
			return a.Users.Total() > b.Users.Total()
		}
		if a.Trips != b.Trips {
			return a.Trips > b.Trips
		}
		return a.Name < b.Name
	})
	for _, d := range byDriver {
		s.Drivers = append(s.Drivers, *d)
	}
	sort.Slice(s.Drivers, func(i, j int) bool {
		a, b := s.Drivers[i], s.Drivers[j]
		if a.Trips != b.Trips {
			return a.Trips > b.Trips
		}
		return a.Name < b.Name
	})
	for _, in := range incidents {
		if _, ok := days[in.At.Format(time.DateOnly)]; ok {
			s.Incidents[in.Kind]++
		}
	}
	if s.Routes == nil {
		s.Routes = []RouteStat{}
	}
	if s.Drivers == nil {
		s.Drivers = []NameStat{}
	}
	return s
}

// StatsSheet is the name of the statistics sheet in the workbooks.
const StatsSheet = "ESTADÍSTICAS"

// StatsWorkbook is a workbook with only the statistics of a period.
func StatsWorkbook(s Stats) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetSheetName("Sheet1", StatsSheet); err != nil {
		return nil, err
	}
	if err := writeStats(f, StatsSheet, s); err != nil {
		return nil, err
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// addStatsSheet appends the statistics sheet to a filled workbook.
func (b *Book) addStatsSheet(s Stats) error {
	if _, err := b.f.NewSheet(StatsSheet); err != nil {
		return err
	}
	b.sheets = append(b.sheets, StatsSheet)
	return writeStats(b.f, StatsSheet, s)
}

// Chart colors: categorical slots in fixed order (hombres, mujeres), one hue for single series.
const (
	colorSeries1 = "2A78D6"
	colorSeries2 = "EB6834"
)

// writeStats lays out the tables (the data the charts point to) and the charts beside them.
func writeStats(f *excelize.File, sh string, s Stats) error {
	st, err := newTableStyles(f)
	if err != nil {
		return err
	}
	title, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "Calibri", Size: 14, Bold: true}})
	if err != nil {
		return err
	}
	note, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Family: "Calibri", Size: 10, Italic: true, Color: "595959"}})
	if err != nil {
		return err
	}
	for col, w := range map[string]float64{"A": 34, "B": 12, "C": 12, "D": 12, "E": 14, "F": 14, "G": 14, "H": 14} {
		if err := f.SetColWidth(sh, col, col, w); err != nil {
			return err
		}
	}
	// Printed landscape, one page wide: tables on the left, charts on the right.
	landscape, one, tall := "landscape", 1, 0
	if err := f.SetPageLayout(sh, &excelize.PageLayoutOptions{Orientation: &landscape, FitToWidth: &one, FitToHeight: &tall}); err != nil {
		return err
	}
	fit := true
	if err := f.SetSheetProps(sh, &excelize.SheetPropsOptions{FitToPage: &fit}); err != nil {
		return err
	}
	set := func(cell string, v any, style int) error {
		if err := f.SetCellValue(sh, cell, v); err != nil {
			return err
		}
		return f.SetCellStyle(sh, cell, cell, style)
	}
	from, _ := time.Parse(time.DateOnly, s.From)
	to, _ := time.Parse(time.DateOnly, s.To)
	if err := set("A1", fmt.Sprintf("ESTADÍSTICAS DE USUARIOS DEL %s AL %s", LongDate(from), LongDate(to)), title); err != nil {
		return err
	}
	if err := set("A2", "Calculado por el sistema con lo que reportan los choferes al iniciar cada vuelta.", note); err != nil {
		return err
	}

	// table writes a header row and the rows under it; returns the first and last data rows.
	row := 4
	table := func(heads []string, rows [][]any) (int, int, error) {
		for i, h := range heads {
			cell, _ := excelize.CoordinatesToCellName(1+i, row)
			if err := set(cell, h, st.head); err != nil {
				return 0, 0, err
			}
		}
		first := row + 1
		for r, vals := range rows {
			for i, v := range vals {
				cell, _ := excelize.CoordinatesToCellName(1+i, first+r)
				style := st.body
				if i == 0 {
					style = st.label
				}
				if err := set(cell, v, style); err != nil {
					return 0, 0, err
				}
			}
		}
		last := first + max(len(rows), 1) - 1
		row = last + 3
		return first, last, nil
	}
	// Charts go one under the other in column J, each below the previous (rows of 20 px).
	chartRow := 4
	chartAt := func(height uint) string {
		cell := fmt.Sprintf("J%d", chartRow)
		chartRow += int(height)/20 + 2
		return cell
	}
	ref := func(col string, a, b int) string { return fmt.Sprintf("'%s'!$%s$%d:$%s$%d", sh, col, a, col, b) }
	title2 := func(t string) excelize.ChartTitle {
		return excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: t, Font: &excelize.Font{Size: 12, Bold: true, Color: "333333"}}}}
	}
	gap := uint(60)
	dim := excelize.ChartDimension{Width: 640, Height: 300}

	// Summary.
	u := s.Users
	start := row
	if _, _, err := table([]string{"RESUMEN", "TOTAL"}, [][]any{
		{"Vueltas", s.Trips}, {"Vueltas con conteo de usuarios", s.Counted}, {"Total de usuarios", u.Total()},
		{"Hombres", u.Men}, {"Mujeres", u.Women}, {"Personas con discapacidad", u.Disability},
		{"Adultos mayores", u.Elderly}, {"Embarazadas", u.Pregnant}, {"Acompañantes", u.Companions},
		{"Promedio de usuarios por vuelta", avg(u.Total(), s.Counted)},
		{"Fallas de rampa reportadas", s.Incidents["ramp_failure"]}, {"Fallas del camión reportadas", s.Incidents["breakdown"]},
		{"Accidentes reportados", s.Incidents["accident"]},
	}); err != nil {
		return err
	}
	if u.Total() > 0 {
		if err := f.AddChart(sh, chartAt(dim.Height), &excelize.Chart{
			Type: excelize.Doughnut, Title: title2("Usuarios por sexo"), Dimension: dim, HoleSize: 60,
			Legend: excelize.ChartLegend{Position: "right"},
			Series: []excelize.ChartSeries{{Name: "Usuarios", Categories: ref("A", start+4, start+5), Values: ref("B", start+4, start+5),
				DataPoint: []excelize.ChartDataPoint{
					{Index: 0, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries1}}},
					{Index: 1, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries2}}}}}},
			PlotArea: excelize.ChartPlotArea{ShowVal: true, ShowPercent: true},
		}); err != nil {
			return err
		}
		if err := f.AddChart(sh, chartAt(dim.Height), &excelize.Chart{
			Type: excelize.Bar, Title: title2("Usuarios por condición"), Dimension: dim, GapWidth: &gap,
			Legend: excelize.ChartLegend{Position: "none"}, XAxis: excelize.ChartAxis{ReverseOrder: true},
			Series: []excelize.ChartSeries{{Name: "Usuarios", Categories: ref("A", start+6, start+9), Values: ref("B", start+6, start+9),
				Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries1}}}},
			PlotArea: excelize.ChartPlotArea{ShowVal: true},
		}); err != nil {
			return err
		}
	}

	// Routes, most used first.
	var rows [][]any
	for _, r := range s.Routes {
		rows = append(rows, []any{r.Name, r.Trips, r.Users.Total(), r.Users.Men, r.Users.Women, r.Users.Disability, avg(r.Users.Total(), r.Trips)})
	}
	start = row
	first, last, err := table([]string{"RUTA", "VUELTAS", "USUARIOS", "HOMBRES", "MUJERES", "DISCAPACIDAD", "PROMEDIO"}, rows)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		h := max(300, 60+28*len(rows))
		if err := f.AddChart(sh, chartAt(uint(h)), &excelize.Chart{
			Type: excelize.BarStacked, Title: title2("Rutas más usadas (usuarios)"), Dimension: excelize.ChartDimension{Width: 640, Height: uint(h)},
			GapWidth: &gap, Legend: excelize.ChartLegend{Position: "bottom"},
			Series: []excelize.ChartSeries{
				{Name: ref("D", start, start), Categories: ref("A", first, last), Values: ref("D", first, last),
					Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries1}}},
				{Name: ref("E", start, start), Categories: ref("A", first, last), Values: ref("E", first, last),
					Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries2}}},
			},
			XAxis: excelize.ChartAxis{ReverseOrder: true},
		}); err != nil {
			return err
		}
	}

	// Days of the period.
	rows = rows[:0]
	for _, d := range s.Days {
		t, _ := time.Parse(time.DateOnly, d.Day)
		rows = append(rows, []any{fmt.Sprintf("%s %02d/%02d", dayAbbr(t), t.Day(), t.Month()), d.Trips, d.Users.Total(), d.Users.Men, d.Users.Women})
	}
	start = row
	first, last, err = table([]string{"DÍA", "VUELTAS", "USUARIOS", "HOMBRES", "MUJERES"}, rows)
	if err != nil {
		return err
	}
	if s.Trips > 0 {
		if err := f.AddChart(sh, chartAt(dim.Height), &excelize.Chart{
			Type: excelize.ColStacked, Title: title2("Usuarios por día"), Dimension: dim, GapWidth: &gap,
			Legend: excelize.ChartLegend{Position: "bottom"},
			Series: []excelize.ChartSeries{
				{Name: ref("D", start, start), Categories: ref("A", first, last), Values: ref("D", first, last),
					Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries1}}},
				{Name: ref("E", start, start), Categories: ref("A", first, last), Values: ref("E", first, last),
					Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries2}}},
			},
		}); err != nil {
			return err
		}
	}

	// Hours of departure, only those with trips.
	rows = rows[:0]
	for _, h := range s.Hours {
		if h.Trips > 0 {
			rows = append(rows, []any{fmt.Sprintf("%02d:00", h.Hour), h.Trips, h.Users})
		}
	}
	start = row
	first, last, err = table([]string{"HORA DE SALIDA", "VUELTAS", "USUARIOS"}, rows)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		if err := f.AddChart(sh, chartAt(dim.Height), &excelize.Chart{
			Type: excelize.Col, Title: title2("Usuarios por hora de salida"), Dimension: dim, GapWidth: &gap,
			Legend: excelize.ChartLegend{Position: "none"},
			Series: []excelize.ChartSeries{{Name: "Usuarios", Categories: ref("A", first, last), Values: ref("C", first, last),
				Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{colorSeries1}}}},
		}); err != nil {
			return err
		}
	}

	// Drivers.
	rows = rows[:0]
	for _, d := range s.Drivers {
		rows = append(rows, []any{d.Name, d.Trips, d.Users})
	}
	_, _, err = table([]string{"CHOFER", "VUELTAS", "USUARIOS"}, rows)
	return err
}

func avg(total, n int) any {
	if n == 0 {
		return 0
	}
	return float64(int(float64(total)/float64(n)*10+0.5)) / 10
}

func dayAbbr(t time.Time) string {
	return [7]string{"DOM", "LUN", "MAR", "MIÉ", "JUE", "VIE", "SÁB"}[t.Weekday()]
}

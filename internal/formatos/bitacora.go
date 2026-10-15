package formatos

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

// Counts are the users of one trip, in the bitácora's categories.
type Counts struct {
	Disability, Elderly, Pregnant, Companions, Men, Women int
}

// Total is everyone on board ("TOTAL DE USUARIOS" = mujeres + hombres); the other categories
// describe those same users.
func (c Counts) Total() int { return c.Men + c.Women }

func (c *Counts) add(o Counts) {
	c.Disability += o.Disability
	c.Elderly += o.Elderly
	c.Pregnant += o.Pregnant
	c.Companions += o.Companions
	c.Men += o.Men
	c.Women += o.Women
}

// Trip is one vuelta: the driver started the route (SALIDA) and ended it (LLEGADA). Times are local.
type Trip struct {
	RouteID string
	Driver  string
	Started time.Time
	Ended   *time.Time
	Users   Counts
	// Counted: the driver gave the users (trips started before the counts were asked have none).
	Counted bool
}

// Note is something that happened on a route (a failure the driver reported), for OBSERVACIONES.
type Note struct {
	RouteID string
	At      time.Time
	Text    string
}

// Week is what the system knows of the week that starts on Monday (local time).
type Week struct {
	Monday time.Time
	Trips  []Trip
	Notes  []Note
	// For the ESTADÍSTICAS sheet: route names and the failures the drivers reported.
	Routes    map[string]RouteInfo
	Incidents []IncidentAt
}

const (
	tripsPerDay = 4
	workDays    = 5
)

var dayNames = [workDays]string{"LUNES", "MARTES", "MIÉRCOLES", "JUEVES", "VIERNES"}
var dayShort = [workDays]string{"LUN", "MAR", "MIÉ", "JUE", "VIE"}

// block is one route of the log on a sheet: the driver's cell, the five day rows (Monday to Friday,
// four trips of LLEGADA/SALIDA in D:K) and the users table under it, which the template has as a
// picture and is redrawn here as cells so it can be filled.
type block struct {
	sheet    string
	routes   []string // system route ids counted in this block
	driver   string   // cell with the driver's name
	firstDay int      // row of Monday
	datos    int      // header row of the DATOS × día table (0: none)
	cats     int      // header row of the users-by-category table (0: none)
	picture  string   // cell of the template picture the tables replace
	obs      string   // OBSERVACIONES box ("B23:L28"), CAS sheets only
	obsPic   string
}

// The department's workbook: three CAS sheets (Gómez Palacio – Lerdo, one route each) and the Durango
// routes stacked on Hoja1 (Hoja2 is the same log with the users by category).
var blocks = []block{
	{sheet: "HUGO CAS", routes: []string{"cas-3"}, driver: "B8", firstDay: 8, datos: 15, picture: "B15", obs: "B23:K27", obsPic: "B23"},
	{sheet: "FERNANDO CAS", routes: []string{"cas-1"}, driver: "B8", firstDay: 8, datos: 15, picture: "A15", obs: "B24:K28", obsPic: "B24"},
	{sheet: "JORGE CAS 2", routes: []string{"cas-2"}, driver: "B8", firstDay: 8, datos: 15, picture: "A15", obs: "B23:K28", obsPic: "B23"},
	{sheet: "Hoja1", routes: []string{"azul-1"}, driver: "B6", firstDay: 6, datos: 12, picture: "A12"},
	{sheet: "Hoja1", routes: []string{"azul-2"}, driver: "B21", firstDay: 21, datos: 26, picture: "B26"},
	{sheet: "Hoja1", routes: []string{"azul-3"}, driver: "B37", firstDay: 37, datos: 43, picture: "A43"},
	{sheet: "Hoja1", routes: []string{"azul-4"}, driver: "B52", firstDay: 52, datos: 57, picture: "B57"},
	{sheet: "Hoja1", routes: []string{"nayar-ferreria"}, driver: "B68", firstDay: 68, datos: 74, picture: "A74"},
	{sheet: "Hoja1", routes: []string{"servicios"}, driver: "B82", firstDay: 82, datos: 87, picture: "B87"},
	{sheet: "Hoja1", routes: []string{"azul-5"}, driver: "B96", firstDay: 96, datos: 101, picture: "B101"},
	{sheet: "Hoja2", routes: []string{"azul-1"}, driver: "B6", firstDay: 6, cats: 11, picture: "B11"},
	{sheet: "Hoja2", routes: []string{"azul-2"}, driver: "B21", firstDay: 21, cats: 27, picture: "B27"},
}

// Period cells of each sheet, written in the template's own style.
var periods = map[string][]string{
	"HUGO CAS": {"A5"}, "FERNANDO CAS": {"A5"}, "JORGE CAS 2": {"A5"},
	"Hoja1": {"A3", "A34", "A65"}, "Hoja2": {"A3"},
}

// Bitacora fills the department's weekly log: the same workbook, with every route's trips, drivers
// and users of the week, plus the panel's overrides.
func Bitacora(w Week, overrides Values) (*Book, error) {
	b, err := open(bitacorasXLSX)
	if err != nil {
		return nil, err
	}
	if err := b.fillBitacora(w); err != nil {
		b.Close()
		return nil, err
	}
	if err := b.apply(overrides); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

func (b *Book) fillBitacora(w Week) error {
	st, err := newTableStyles(b.f)
	if err != nil {
		return err
	}
	b.dropVML = map[string]bool{"Hoja2": true}
	friday := w.Monday.AddDate(0, 0, workDays-1)
	for sheet, cells := range periods {
		text := fmt.Sprintf("PERIODO SEMANAL DEL %d DE %s AL %d DE %s DEL %d", w.Monday.Day(), months[w.Monday.Month()-1],
			friday.Day(), months[friday.Month()-1], friday.Year())
		if !strings.Contains(sheet, "CAS") {
			text = fmt.Sprintf("PERIODO SEMANAL DEL: %d DE: %s AL: %d DE: %s DE %d.", w.Monday.Day(), months[w.Monday.Month()-1],
				friday.Day(), months[friday.Month()-1], friday.Year())
		}
		for _, c := range cells {
			if err := b.field(sheet, c, text); err != nil {
				return err
			}
		}
	}

	// Trips by route, Monday to Friday, in order.
	byRoute := map[string][]Trip{}
	var week [workDays]Counts
	for _, t := range w.Trips {
		d := dayIndex(w.Monday, t.Started)
		if d < 0 || d >= workDays {
			continue
		}
		byRoute[t.RouteID] = append(byRoute[t.RouteID], t)
		week[d].add(t.Users)
	}
	for _, ts := range byRoute {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Started.Before(ts[j].Started) })
	}

	for _, bl := range blocks {
		var trips []Trip
		for _, id := range bl.routes {
			trips = append(trips, byRoute[id]...)
		}
		if err := b.fillBlock(st, w.Monday, bl, trips, w.Notes); err != nil {
			return fmt.Errorf("%s fila %d: %w", bl.sheet, bl.firstDay, err)
		}
	}

	// Hoja3: the week's users by category, all routes.
	for d := 0; d < workDays; d++ {
		c := week[d]
		for i, v := range []int{c.Disability, c.Elderly, c.Pregnant, c.Companions, c.Men, c.Women, c.Total()} {
			cell, _ := excelize.CoordinatesToCellName(2+i, 3+d)
			if err := b.field("Hoja3", cell, blankZero(v, c.Total() > 0)); err != nil {
				return err
			}
		}
	}

	// ESTADÍSTICAS: the week (Monday to Sunday) in numbers and charts.
	if err := b.addStatsSheet(Summarize(w.Monday, w.Monday.AddDate(0, 0, 6), w.Trips, w.Incidents, w.Routes)); err != nil {
		return err
	}

	// Hoja4 is the blank vehicle report that travels with the log: fillable as it is.
	for _, rng := range []string{"A2:A5", "A8:A15"} {
		if err := b.fillable("Hoja4", rng); err != nil {
			return err
		}
	}
	return nil
}

func (b *Book) fillBlock(st tableStyles, monday time.Time, bl block, trips []Trip, notes []Note) error {
	sh := bl.sheet
	// Driver: whoever drove it this week; the template's name while nobody has.
	var driver any
	if names := drivers(trips); names != "" {
		driver = names
	}
	if err := b.field(sh, bl.driver, driver); err != nil {
		return err
	}

	var perDay [workDays]Counts
	var tripsDay [workDays]int
	times := map[string]string{}
	for _, t := range trips {
		d := dayIndex(monday, t.Started)
		perDay[d].add(t.Users)
		n := tripsDay[d]
		tripsDay[d]++
		if n >= tripsPerDay {
			continue // the form has room for four; the panel lists the rest
		}
		row := bl.firstDay + d
		arrival, _ := excelize.CoordinatesToCellName(4+2*n, row) // D, F, H, J: LLEGADA
		departure, _ := excelize.CoordinatesToCellName(5+2*n, row)
		if t.Ended != nil {
			times[arrival] = t.Ended.Format("15:04")
		}
		times[departure] = t.Started.Format("15:04")
	}
	for d := 0; d < workDays; d++ {
		for col := 4; col <= 11; col++ {
			cell, _ := excelize.CoordinatesToCellName(col, bl.firstDay+d)
			if err := b.field(sh, cell, times[cell]); err != nil {
				return err
			}
			if err := centerCell(b.f, sh, cell); err != nil {
				return err
			}
		}
	}

	if bl.picture != "" {
		if err := b.f.DeletePicture(sh, bl.picture); err != nil {
			return err
		}
	}
	if bl.datos > 0 {
		if err := b.datosTable(st, sh, bl.datos, perDay, tripsDay); err != nil {
			return err
		}
	}
	if bl.cats > 0 {
		if err := b.catsTable(st, sh, bl.cats, perDay); err != nil {
			return err
		}
	}
	if bl.obs != "" {
		if err := b.f.DeletePicture(sh, bl.obsPic); err != nil {
			return err
		}
		if err := b.obsBox(st, sh, bl, trips, notes, monday); err != nil {
			return err
		}
	}
	return nil
}

// datosTable redraws "DATOS | LUNES … VIERNES" (MUJERES, HOMBRES, TOTAL DE USUARIOS, VUELTAS) as cells.
func (b *Book) datosTable(st tableStyles, sh string, row int, perDay [workDays]Counts, trips [workDays]int) error {
	cols := [workDays][2]string{{"C", "D"}, {"E", "F"}, {"G", "H"}, {"I", "J"}, {"K", "K"}}
	if err := put(b.f, sh, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), st.head, "DATOS"); err != nil {
		return err
	}
	for d, c := range cols {
		if err := put(b.f, sh, fmt.Sprintf("%s%d", c[0], row), fmt.Sprintf("%s%d", c[1], row), st.head, dayNames[d]); err != nil {
			return err
		}
	}
	rows := []struct {
		label string
		value func(d int) int
	}{
		{"MUJERES", func(d int) int { return perDay[d].Women }},
		{"HOMBRES", func(d int) int { return perDay[d].Men }},
		{"TOTAL DE USUARIOS", func(d int) int { return perDay[d].Total() }},
		{"VUELTAS", func(d int) int { return trips[d] }},
	}
	for i, rw := range rows {
		r := row + 1 + i
		if err := put(b.f, sh, fmt.Sprintf("B%d", r), fmt.Sprintf("B%d", r), st.label, rw.label); err != nil {
			return err
		}
		for d, c := range cols {
			from := fmt.Sprintf("%s%d", c[0], r)
			if err := put(b.f, sh, from, fmt.Sprintf("%s%d", c[1], r), st.body, nil); err != nil {
				return err
			}
			if err := b.field(sh, from, blankZero(rw.value(d), trips[d] > 0)); err != nil {
				return err
			}
		}
	}
	return nil
}

// catsTable redraws "DIAS | DISCAPACIDAD … TOTAL" (the users by category) as cells.
func (b *Book) catsTable(st tableStyles, sh string, row int, perDay [workDays]Counts) error {
	heads := []struct{ from, to, title string }{{"B", "B", "DIAS"}, {"C", "C", "DISCAPACIDAD"}, {"D", "D", "ADULTOS MAYORES"},
		{"E", "E", "EMBARAZADAS"}, {"F", "F", "ACOMPAÑANTES"}, {"G", "G", "HOMBRES"}, {"H", "H", "MUJERES"}, {"I", "J", "TOTAL"}}
	for _, h := range heads {
		if err := put(b.f, sh, fmt.Sprintf("%s%d", h.from, row), fmt.Sprintf("%s%d", h.to, row), st.head, h.title); err != nil {
			return err
		}
	}
	if err := b.f.SetRowHeight(sh, row, 30); err != nil { // two-line titles, as in the template's picture
		return err
	}
	for d := 0; d < workDays; d++ {
		r := row + 1 + d
		c := perDay[d]
		if err := put(b.f, sh, fmt.Sprintf("B%d", r), fmt.Sprintf("B%d", r), st.label, dayNames[d]); err != nil {
			return err
		}
		for i, v := range []int{c.Disability, c.Elderly, c.Pregnant, c.Companions, c.Men, c.Women, c.Total()} {
			h := heads[i+1]
			from := fmt.Sprintf("%s%d", h.from, r)
			if err := put(b.f, sh, from, fmt.Sprintf("%s%d", h.to, r), st.body, nil); err != nil {
				return err
			}
			if err := b.field(sh, from, blankZero(v, c.Total() > 0)); err != nil {
				return err
			}
		}
	}
	return nil
}

// obsBox redraws the OBSERVACIONES box, filled with what the drivers reported on the route that week.
func (b *Book) obsBox(st tableStyles, sh string, bl block, trips []Trip, notes []Note, monday time.Time) error {
	from, to, _ := strings.Cut(bl.obs, ":")
	_, r1, _ := excelize.CellNameToCoordinates(from)
	_, r2, _ := excelize.CellNameToCoordinates(to)
	tc, _, _ := excelize.CellNameToCoordinates(to)
	lastCol, _ := excelize.ColumnNumberToName(tc)
	if err := put(b.f, sh, fmt.Sprintf("B%d", r1), fmt.Sprintf("B%d", r2), st.total, "OBSERVACIONES"); err != nil {
		return err
	}
	box := fmt.Sprintf("C%d", r1)
	if err := put(b.f, sh, box, fmt.Sprintf("%s%d", lastCol, r2), st.box, nil); err != nil {
		return err
	}
	routes := map[string]bool{}
	for _, id := range bl.routes {
		routes[id] = true
	}
	var lines []string
	for _, n := range notes {
		d := dayIndex(monday, n.At)
		if routes[n.RouteID] && d >= 0 && d < workDays {
			lines = append(lines, fmt.Sprintf("%s %s: %s", dayShort[d], n.At.Format("15:04"), strings.ToUpper(n.Text)))
		}
	}
	return b.field(sh, box, strings.Join(lines, "\n"))
}

// drivers lists who drove, in order of first trip: "HUGO ALONSO ESTEVANE ARROYO / JORGE SOTO".
func drivers(trips []Trip) string {
	seen := map[string]bool{}
	var out []string
	for _, t := range trips {
		n := strings.ToUpper(strings.TrimSpace(t.Driver))
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return strings.Join(out, " / ")
}

// blankZero leaves a day without trips empty instead of filling it with zeros.
func blankZero(n int, any bool) string {
	if !any && n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

func dayIndex(monday, t time.Time) int {
	y, m, d := t.Date()
	return int(time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Sub(
		time.Date(monday.Year(), monday.Month(), monday.Day(), 12, 0, 0, 0, time.UTC)).Hours() / 24)
}

type tableStyles struct{ head, body, label, total, box int }

// newTableStyles copies the look of the template's tables: blue header (#00B0F0) with white Calibri
// 11, thin black borders.
func newTableStyles(f *excelize.File) (tableStyles, error) {
	border := []excelize.Border{{Type: "left", Style: 1, Color: "000000"}, {Type: "right", Style: 1, Color: "000000"},
		{Type: "top", Style: 1, Color: "000000"}, {Type: "bottom", Style: 1, Color: "000000"}}
	var st tableStyles
	var err error
	if st.head, err = f.NewStyle(&excelize.Style{Border: border, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"00B0F0"}},
		Font:      &excelize.Font{Family: "Calibri", Size: 11, Color: "FFFFFF"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true}}); err != nil {
		return st, err
	}
	if st.body, err = f.NewStyle(&excelize.Style{Border: border, Font: &excelize.Font{Family: "Calibri", Size: 11},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"}}); err != nil {
		return st, err
	}
	if st.label, err = f.NewStyle(&excelize.Style{Border: border, Font: &excelize.Font{Family: "Calibri", Size: 11},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "center"}}); err != nil {
		return st, err
	}
	if st.total, err = f.NewStyle(&excelize.Style{Border: border, Font: &excelize.Font{Family: "Calibri", Size: 11, Bold: true},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"}}); err != nil {
		return st, err
	}
	st.box, err = f.NewStyle(&excelize.Style{Border: border, Font: &excelize.Font{Family: "Calibri", Size: 11},
		Alignment: &excelize.Alignment{Horizontal: "left", Vertical: "top", WrapText: true}})
	return st, err
}

// put styles a (possibly merged) range and writes v (nil leaves the value alone).
func put(f *excelize.File, sh, from, to string, style int, v any) error {
	if from != to {
		if err := f.MergeCell(sh, from, to); err != nil {
			return err
		}
	}
	if err := f.SetCellStyle(sh, from, to, style); err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	return f.SetCellValue(sh, from, v)
}

func centerCell(f *excelize.File, sh, cell string) error {
	id, err := f.GetCellStyle(sh, cell)
	if err != nil {
		return err
	}
	st, err := f.GetStyle(id)
	if err != nil {
		return err
	}
	if st.Alignment == nil {
		st.Alignment = &excelize.Alignment{}
	}
	if st.Alignment.Horizontal == "center" && st.Alignment.Vertical == "center" {
		return nil
	}
	st.Alignment.Horizontal, st.Alignment.Vertical = "center", "center"
	nid, err := f.NewStyle(st)
	if err != nil {
		return err
	}
	return f.SetCellStyle(sh, cell, cell, nid)
}

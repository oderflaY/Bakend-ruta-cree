package formatos

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func reopen(t *testing.T, b *Book) *excelize.File {
	t.Helper()
	data, err := b.XLSX()
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("FORMATOS_OUT"); dir != "" {
		os.WriteFile(dir+"/"+b.sheets[0]+".xlsx", data, 0o644)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func check(t *testing.T, f *excelize.File, sh, cell, want string) {
	t.Helper()
	if got, _ := f.GetCellValue(sh, cell); got != want {
		t.Errorf("%s!%s = %q, want %q", sh, cell, got, want)
	}
}

func TestBitacoraKeepsTheWorkbookAndFillsIt(t *testing.T) {
	loc := time.FixedZone("CST", -6*3600)
	monday := time.Date(2026, 10, 12, 0, 0, 0, 0, loc)
	at := func(day, h, m int) time.Time {
		return monday.AddDate(0, 0, day).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	end := func(day, h, m int) *time.Time { t := at(day, h, m); return &t }
	w := Week{Monday: monday, Trips: []Trip{
		{RouteID: "cas-3", Driver: "Hugo Alonso Estevane Arroyo", Started: at(0, 7, 5), Ended: end(0, 8, 40), Users: Counts{Disability: 3, Elderly: 2, Companions: 3, Men: 4, Women: 4}},
		{RouteID: "cas-3", Driver: "Hugo Alonso Estevane Arroyo", Started: at(0, 9, 0), Ended: end(0, 10, 30), Users: Counts{Pregnant: 1, Women: 1}},
		{RouteID: "azul-4", Driver: "Oscar Gerardo Sanchez Carrillo", Started: at(2, 7, 0), Users: Counts{Men: 2, Women: 1, Disability: 1}},
		{RouteID: "azul-1", Driver: "Fernando Vargas", Started: at(4, 8, 0), Ended: end(4, 9, 0), Users: Counts{Men: 1, Elderly: 1}},
		{RouteID: "azul-4", Started: at(5, 7, 0)}, // Saturday: not on the form
	}, Notes: []Note{{RouteID: "cas-3", At: at(1, 11, 15), Text: "Falla de rampa (Unidad 22)"}}}

	b, err := Bitacora(w, Values{"HUGO CAS": {"E9": "07:30", "Z99": "ignorado"}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	f := reopen(t, b)
	want := []string{"HUGO CAS", "Hoja4", "Hoja1", "Hoja2", "Hoja3", "FERNANDO CAS", "JORGE CAS 2", "ESTADÍSTICAS"}
	if got := f.GetSheetList(); len(got) != len(want) {
		t.Fatalf("sheets = %q", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("sheets = %q", got)
			}
		}
	}
	check(t, f, "HUGO CAS", "A5", "PERIODO SEMANAL DEL 12 DE OCTUBRE AL 16 DE OCTUBRE DEL 2026")
	check(t, f, "HUGO CAS", "A8", "3 CAS")
	check(t, f, "HUGO CAS", "B8", "HUGO ALONSO ESTEVANE ARROYO")
	check(t, f, "HUGO CAS", "D8", "08:40")
	check(t, f, "HUGO CAS", "E8", "07:05")
	check(t, f, "HUGO CAS", "F8", "10:30")
	check(t, f, "HUGO CAS", "E9", "07:30") // typed in the panel
	check(t, f, "HUGO CAS", "B16", "MUJERES")
	check(t, f, "HUGO CAS", "C16", "5")
	check(t, f, "HUGO CAS", "C18", "9")
	check(t, f, "HUGO CAS", "C19", "2")
	check(t, f, "HUGO CAS", "E19", "") // no trips on Tuesday
	check(t, f, "HUGO CAS", "C23", "MAR 11:15: FALLA DE RAMPA (UNIDAD 22)")
	check(t, f, "FERNANDO CAS", "B8", "GERARDO CERVANTES DORADO ") // nobody drove 1 CAS: the template's driver
	check(t, f, "Hoja1", "A3", "PERIODO SEMANAL DEL: 12 DE: OCTUBRE AL: 16 DE: OCTUBRE DE 2026.")
	// ESTADÍSTICAS: the whole week, Saturday included.
	check(t, f, StatsSheet, "A1", "ESTADÍSTICAS DE USUARIOS DEL 12 DE OCTUBRE DE 2026 AL 18 DE OCTUBRE DE 2026")
	check(t, f, StatsSheet, "B5", "5")      // vueltas
	check(t, f, StatsSheet, "B7", "13")     // usuarios: 8 + 1 + 3 + 1
	check(t, f, StatsSheet, "A21", "cas-3") // most used route first (no names in this test)
	check(t, f, StatsSheet, "C21", "9")
	check(t, f, "Hoja1", "B52", "OSCAR GERARDO SANCHEZ CARRILLO")
	check(t, f, "Hoja1", "E54", "07:00")
	check(t, f, "Hoja1", "D54", "")
	check(t, f, "Hoja1", "G60", "3")     // route 4 users table, Wednesday total
	check(t, f, "Hoja2", "D10", "09:00") // Hoja2 route 1, Friday arrival
	check(t, f, "Hoja2", "E10", "08:00")
	check(t, f, "Hoja2", "D16", "1") // categories: adultos mayores on Friday
	check(t, f, "Hoja3", "B3", "3")
	check(t, f, "Hoja3", "H3", "9")
	check(t, f, "Hoja3", "H6", "")
	for _, sh := range []string{"HUGO CAS", "Hoja1", "Hoja2"} {
		cells, _ := f.GetPictureCells(sh)
		for _, c := range cells {
			if c != "B1" && c != "I1" && c != "C1" && c != "J1" && c != "B32" && c != "I32" && c != "B63" && c != "I63" && c != "B2" {
				t.Errorf("%s still has the picture at %s", sh, c)
			}
		}
		if len(cells) < 2 {
			t.Errorf("%s lost its logos: %v", sh, cells)
		}
	}

	views, err := b.Views()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 8 || views[0].Name != "HUGO CAS" || len(views[0].Pictures) != 2 {
		t.Fatalf("views: %d, first %q, pictures %d", len(views), views[0].Name, len(views[0].Pictures))
	}
	var e9 *CellView
	for i, c := range views[0].Cells {
		if c.Ref == "E9" {
			e9 = &views[0].Cells[i]
		}
	}
	if e9 == nil || !e9.Fillable || e9.Value != "07:30" || e9.Auto != "" {
		t.Fatalf("E9 view: %+v", e9)
	}
	if clean := b.Clean(Values{"HUGO CAS": {"E8": "07:05", "E9": "07:30", "A1": "x"}}); len(clean["HUGO CAS"]) != 1 || clean["HUGO CAS"]["E9"] != "07:30" {
		t.Fatalf("clean: %v", clean)
	}
}

func TestOrdenYVehiculo(t *testing.T) {
	km := 154320
	bus := Bus{Number: "25-1451", Plates: "13-EZC-C", Brand: "MERCEDES", Model: "SPRINTER"}
	o, err := Orden(ServiceOrder{Date: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), Km: &km, Partida: "3551", Recurso: "ESTATAL",
		Execution: "Mantenimiento proveedor", Bus: bus, Items: []ServiceItem{{"Cambio de aceite y filtros", "SERVICIO", "1"}, {"Balatas delanteras", "JUEGO", "1"}}},
		Values{"Hoja1": {"C24": "LIC. OTRA PERSONA"}})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	f := reopen(t, o)
	for cell, want := range map[string]string{"F12": "12/10/2026", "F11": "154320", "B15": "MERCEDES SPRINTER No. ECONOMICO 25-1451 PLACAS: 13-EZC-C",
		"A18": "1", "B18": "CAMBIO DE ACEITE Y FILTROS", "E19": "JUEGO", "A20": "", "C24": "LIC. OTRA PERSONA", "C23": "L.B. ERICK ALFREDO PALACIOS SANCHEZ ", "F16": "X"} {
		check(t, f, "Hoja1", cell, want)
	}
	if v, err := o.Views(); err != nil || len(v) != 1 || len(v[0].Pictures) == 0 {
		t.Fatalf("orden view: %v %v", err, v)
	}

	v, err := Vehiculo(VehicleReport{Date: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), Bus: bus, Observations: "Falla de rampa en la parada CREE.\nSe reportó al despacho."}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	g := reopen(t, v)
	if l := g.GetSheetList(); len(l) != 1 || l[0] != "Hoja4" {
		t.Fatalf("sheets = %q", l)
	}
	check(t, g, "Hoja4", "A3", "No. Economico: 25-1451")
	check(t, g, "Hoja4", "A8", "FALLA DE RAMPA EN LA PARADA CREE.")
	check(t, g, "Hoja4", "A9", "SE REPORTÓ AL DESPACHO.")
	if got, _ := g.GetCellValue("Hoja4", "A10"); len(got) < 20 || got[0] != '_' {
		t.Errorf("unused line should keep the form's underscores: %q", got)
	}
}

func TestSummarizeCountsWhatTheDriversReported(t *testing.T) {
	loc := time.FixedZone("CST", -6*3600)
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 15, 0, 0, loc) }
	trips := []Trip{
		{RouteID: "cas-1", Driver: "Ana", Started: day(12, 7), Users: Counts{Men: 3, Women: 5, Disability: 2}, Counted: true},
		{RouteID: "cas-1", Driver: "ana ", Started: day(12, 9), Users: Counts{Men: 1, Women: 1}, Counted: true},
		{RouteID: "azul-4", Driver: "Luis", Started: day(14, 7), Users: Counts{Women: 2, Pregnant: 1}, Counted: true},
		{RouteID: "azul-4", Driver: "Luis", Started: day(14, 13)},                                      // no counts
		{RouteID: "azul-4", Driver: "Luis", Started: day(20, 7), Users: Counts{Men: 9}, Counted: true}, // outside
	}
	inc := []IncidentAt{{Kind: "ramp_failure", At: day(13, 10)}, {Kind: "breakdown", At: day(25, 10)}}
	s := Summarize(time.Date(2026, 10, 12, 0, 0, 0, 0, loc), time.Date(2026, 10, 18, 0, 0, 0, 0, loc), trips, inc,
		map[string]RouteInfo{"cas-1": {Name: "Ruta 1 CAS", Color: "#F4511E"}})
	if s.Trips != 4 || s.Counted != 3 {
		t.Errorf("vueltas %d, con conteo %d", s.Trips, s.Counted)
	}
	if s.Users.Total() != 12 || s.Users.Women != 8 || s.Users.Disability != 2 || s.Users.Pregnant != 1 {
		t.Errorf("usuarios %+v", s.Users)
	}
	if len(s.Routes) != 2 || s.Routes[0].Name != "Ruta 1 CAS" || s.Routes[0].Users.Total() != 10 || s.Routes[1].Name != "azul-4" {
		t.Errorf("rutas %+v", s.Routes)
	}
	if len(s.Days) != 7 || s.Days[0].Users.Total() != 10 || s.Days[2].Trips != 2 || s.Days[6].Trips != 0 {
		t.Errorf("días %+v", s.Days)
	}
	if s.Hours[7].Trips != 2 || s.Hours[7].Users != 10 {
		t.Errorf("7:00 %+v", s.Hours[7])
	}
	if s.Week[0].Users.Total() != 10 || s.Week[2].Trips != 2 {
		t.Errorf("semana %+v", s.Week)
	}
	if len(s.Drivers) != 2 || s.Drivers[0].Name != "ANA" || s.Drivers[0].Trips != 2 {
		t.Errorf("choferes %+v", s.Drivers)
	}
	if s.Incidents["ramp_failure"] != 1 || s.Incidents["breakdown"] != 0 {
		t.Errorf("incidentes %+v", s.Incidents)
	}
	data, err := StatsWorkbook(s)
	if err != nil {
		t.Fatal(err)
	}
	if dir := os.Getenv("FORMATOS_OUT"); dir != "" {
		os.WriteFile(dir+"/estadisticas.xlsx", data, 0o644)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	check(t, f, StatsSheet, "B7", "12")
	check(t, f, StatsSheet, "A21", "Ruta 1 CAS")
}

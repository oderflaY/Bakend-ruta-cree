package admin

import (
	"bytes"
	"testing"

	"github.com/xuri/excelize/v2"
)

// Same layout as the DIF "PARQUE VEHICULAR" sheet: an untitled first block, then titled blocks.
func TestParseFleet(t *testing.T) {
	f := excelize.NewFile()
	hdr := []any{"No.", "# ECONOMICO ", "#PLACA", "#SERIE", "No. CAMION ", "MODELO ", "AÑO"}
	rows := [][]any{
		hdr,
		{1, "25-3369", "FU-8971-B", "3HJFC9JK1GSS10661", 3, "HINO", 2016},
		{2, "25-3376", "FU-8973-B", "WV1LH22E4H6003122", "N/A", "VOLSKWAGEN CRAFTER", 2017},
		{"CENTRO DE ATENCION Y SOCIALIZACION "},
		hdr,
		{2, "25-1451", "13-EZC-C", "3MBAA2DM1CM042894", "N/A", "MERCEDEZ", 2014},
		{"C.R.E.E. SANTIAGO PAPASQUIARO "},
		{"COORDINADOR DE CENTRO: DR. ALGUIEN "},
		hdr,
		{1, "25-3371", "FU-8972-B", "WV1LH22E8H6003107", nil, "VOLSKWAGEN CRAFTER", 2017},
	}
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	got, err := ParseFleet(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d units: %+v", len(got), got)
	}
	want := []struct{ num, centro, unit, brand, model string }{
		{"25-3369", "CREE DURANGO", "3", "HINO", ""},
		{"25-3376", "CREE DURANGO", "", "VOLKSWAGEN", "CRAFTER"},
		{"25-1451", "CENTRO DE ATENCION Y SOCIALIZACION", "", "MERCEDES", ""},
		{"25-3371", "C.R.E.E. SANTIAGO PAPASQUIARO", "", "VOLKSWAGEN", "CRAFTER"},
	}
	for i, w := range want {
		g := got[i]
		if g.Number != w.num || g.Centro != w.centro || g.UnitNo != w.unit || g.Brand != w.brand || g.Model != w.model {
			t.Errorf("unit %d = %+v, want %+v", i, g, w)
		}
	}
	if got[0].Year == nil || *got[0].Year != 2016 || got[0].Plates != "FU-8971-B" || got[0].Serial != "3HJFC9JK1GSS10661" {
		t.Errorf("unit 0 fields: %+v", got[0])
	}
}

// Package formatos fills the DIF Estatal's Ruta Azul paper forms with the system's data: the weekly
// trip log (bitácora), the service order (solicitud y autorización del servicio) and the vehicle
// report. The department's own Excel files are the templates, so a download is the same workbook
// (sheets, layout, logos) with the blanks filled in.
//
// Every fillable cell gets an automatic value from the system and can be overwritten from the web
// panel; the overrides are stored apart (Values) and win over the automatic value.
package formatos

import (
	"bytes"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

//go:embed plantillas/bitacoras.xlsx
var bitacorasXLSX []byte

//go:embed plantillas/orden_servicio.xlsx
var ordenXLSX []byte

var months = [...]string{"ENERO", "FEBRERO", "MARZO", "ABRIL", "MAYO", "JUNIO", "JULIO", "AGOSTO",
	"SEPTIEMBRE", "OCTUBRE", "NOVIEMBRE", "DICIEMBRE"}

// LongDate writes a date the way the forms do: "12 DE OCTUBRE DE 2026".
func LongDate(t time.Time) string {
	return fmt.Sprintf("%d DE %s DE %d", t.Day(), months[t.Month()-1], t.Year())
}

// Values are cell contents typed in the panel, by sheet and cell ("D8"). They win over the
// automatic values; an empty string clears a cell.
type Values map[string]map[string]string

// Book is a filled workbook: the template plus automatic values plus the panel's overrides.
type Book struct {
	f      *excelize.File
	tpl    []byte
	sheets []string // visible sheets, in order
	// Sheets whose VML pictures go away on download (see dropLegacyDrawings).
	dropVML map[string]bool
	auto    map[string]map[string]string
	fields  map[string]map[string]bool // fillable cells
}

func open(tpl []byte) (*Book, error) {
	f, err := excelize.OpenReader(bytes.NewReader(tpl))
	if err != nil {
		return nil, err
	}
	return &Book{f: f, tpl: tpl, sheets: f.GetSheetList(), auto: map[string]map[string]string{},
		fields: map[string]map[string]bool{}}, nil
}

// Close releases the workbook.
func (b *Book) Close() { b.f.Close() }

// keepOnly drops every other sheet of the template.
func (b *Book) keepOnly(names ...string) error {
	keep := map[string]bool{}
	for _, n := range names {
		keep[n] = true
	}
	var left []string
	for _, sh := range b.f.GetSheetList() {
		if keep[sh] {
			left = append(left, sh)
			continue
		}
		if err := b.f.DeleteSheet(sh); err != nil {
			return err
		}
	}
	b.sheets = left
	if len(left) > 0 {
		idx, _ := b.f.GetSheetIndex(left[0])
		b.f.SetActiveSheet(idx)
	}
	return nil
}

// field marks a cell fillable and writes its automatic value (nil keeps the template's content).
func (b *Book) field(sheet, cell string, auto any) error {
	if b.fields[sheet] == nil {
		b.fields[sheet], b.auto[sheet] = map[string]bool{}, map[string]string{}
	}
	cell = strings.ToUpper(cell)
	b.fields[sheet][cell] = true
	switch v := auto.(type) {
	case nil:
		s, _ := b.f.GetCellValue(sheet, cell)
		b.auto[sheet][cell] = s
		return nil
	case string:
		b.auto[sheet][cell] = v
	default:
		b.auto[sheet][cell] = fmt.Sprint(v)
	}
	return setValue(b.f, sheet, cell, b.auto[sheet][cell])
}

// fillable marks every cell of a range fillable, keeping the template's content as automatic value.
func (b *Book) fillable(sheet, rng string) error {
	from, to, _ := strings.Cut(rng, ":")
	if to == "" {
		to = from
	}
	c1, r1, err := excelize.CellNameToCoordinates(from)
	if err != nil {
		return err
	}
	c2, r2, err := excelize.CellNameToCoordinates(to)
	if err != nil {
		return err
	}
	merges, _ := b.f.GetMergeCells(sheet)
	for r := r1; r <= r2; r++ {
		for c := c1; c <= c2; c++ {
			cell, _ := excelize.CoordinatesToCellName(c, r)
			if covered(merges, cell) || b.fields[sheet][cell] {
				continue
			}
			if err := b.field(sheet, cell, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// apply writes the panel's overrides on fillable cells (others are ignored).
func (b *Book) apply(v Values) error {
	for sheet, cells := range v {
		for cell, val := range cells {
			cell = strings.ToUpper(cell)
			if !b.fields[sheet][cell] {
				continue
			}
			if err := setValue(b.f, sheet, cell, val); err != nil {
				return err
			}
		}
	}
	return nil
}

// Clean keeps only overrides on fillable cells that differ from the automatic value, so a cell
// set back to what the system computed follows the system again.
func (b *Book) Clean(v Values) Values {
	out := Values{}
	for sheet, cells := range v {
		for cell, val := range cells {
			cell = strings.ToUpper(cell)
			if !b.fields[sheet][cell] || utf8.RuneCountInString(val) > 2000 {
				continue
			}
			if strings.TrimSpace(val) == strings.TrimSpace(b.auto[sheet][cell]) {
				continue
			}
			if out[sheet] == nil {
				out[sheet] = map[string]string{}
			}
			out[sheet][cell] = val
		}
	}
	return out
}

// XLSX is the workbook to download.
func (b *Book) XLSX() ([]byte, error) {
	var buf bytes.Buffer
	if err := b.f.Write(&buf); err != nil {
		return nil, err
	}
	if len(b.dropVML) > 0 {
		return dropLegacyDrawings(buf.Bytes(), b.dropVML)
	}
	return buf.Bytes(), nil
}

// setValue writes whole numbers as numbers (so Excel can add them) and everything else as text.
func setValue(f *excelize.File, sheet, cell, s string) error {
	if t := strings.TrimSpace(s); t != "" && len(t) < 10 && (t == "0" || !strings.HasPrefix(t, "0")) {
		if n, err := strconv.Atoi(t); err == nil {
			return f.SetCellInt(sheet, cell, int64(n))
		}
	}
	return f.SetCellStr(sheet, cell, s)
}

// covered reports whether cell is inside a merged range but not its top-left corner.
func covered(merges []excelize.MergeCell, cell string) bool {
	for _, m := range merges {
		if m.GetStartAxis() != cell && inRange(cell, m.GetStartAxis(), m.GetEndAxis()) {
			return true
		}
	}
	return false
}

func inRange(cell, from, to string) bool {
	c, r, err := excelize.CellNameToCoordinates(cell)
	if err != nil {
		return false
	}
	c1, r1, _ := excelize.CellNameToCoordinates(from)
	c2, r2, _ := excelize.CellNameToCoordinates(to)
	return c >= c1 && c <= c2 && r >= r1 && r <= r2
}

// wrap splits text into lines of at most width characters, at spaces, keeping the user's line breaks.
func wrap(text string, width int) []string {
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line := ""
		for _, w := range strings.Fields(para) {
			switch {
			case line == "":
				line = w
			case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) <= width:
				line += " " + w
			default:
				out = append(out, line)
				line = w
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Bus identifies the vehicle on the forms.
type Bus struct {
	Number, Plates, Brand, Model string
}

// Label is the vehicle line of the service order: "MERCEDES No. ECONOMICO 25-1451 PLACAS: 13-EZC-C".
func (b Bus) Label() string {
	s := strings.TrimSpace(b.Brand + " " + b.Model)
	if s != "" {
		s += " "
	}
	s += "No. ECONOMICO " + b.Number
	if b.Plates != "" {
		s += " PLACAS: " + b.Plates
	}
	return s
}

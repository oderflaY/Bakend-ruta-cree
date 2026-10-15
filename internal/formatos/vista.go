package formatos

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"io"
	"math"
	"path"
	"strings"

	"github.com/xuri/excelize/v2"
)

// SheetView is a sheet as the panel draws it: the same grid as Excel (column widths and row heights
// in pixels, merged cells, fonts, fills, borders, logos), with the fillable cells marked.
type SheetView struct {
	Name     string        `json:"name"`
	Cols     []int         `json:"cols"`
	Rows     []int         `json:"rows"`
	Cells    []CellView    `json:"cells"`
	Styles   []CellStyle   `json:"styles"`
	Pictures []PictureView `json:"pictures"`
}

// CellView is one non-empty, styled or fillable cell (1-based row and column).
type CellView struct {
	Ref   string `json:"ref"`
	Row   int    `json:"r"`
	Col   int    `json:"c"`
	Value string `json:"v,omitempty"`
	// Merged cells: how many columns and rows the cell covers.
	ColSpan int `json:"cs,omitempty"`
	RowSpan int `json:"rs,omitempty"`
	Style   int `json:"s"`
	// Fillable from the panel; Auto is what the system put there.
	Fillable bool   `json:"f,omitempty"`
	Auto     string `json:"a,omitempty"`
}

// CellStyle is the part of an Excel style the panel draws.
type CellStyle struct {
	Bold      bool    `json:"b,omitempty"`
	Italic    bool    `json:"i,omitempty"`
	Underline bool    `json:"u,omitempty"`
	Size      float64 `json:"sz,omitempty"`
	Font      string  `json:"ff,omitempty"`
	Color     string  `json:"c,omitempty"`
	Fill      string  `json:"bg,omitempty"`
	HAlign    string  `json:"ha,omitempty"`
	VAlign    string  `json:"va,omitempty"`
	Wrap      bool    `json:"wr,omitempty"`
	// Border widths in pixels: left, right, top, bottom.
	Borders [4]int `json:"bd"`
}

// PictureView is a logo placed at a cell, with its offset and size in pixels.
type PictureView struct {
	Row    int    `json:"r"`
	Col    int    `json:"c"`
	DX     int    `json:"dx"`
	DY     int    `json:"dy"`
	Width  int    `json:"w"`
	Height int    `json:"h"`
	Src    string `json:"src"`
	// Crop of the image as Excel shows it, in fractions (0–1) cut from left, top, right, bottom.
	Crop [4]float64 `json:"crop"`
}

// Views draws every sheet of the book for the panel.
func (b *Book) Views() ([]SheetView, error) {
	anchors := templateAnchors(b.tpl)
	var out []SheetView
	for _, sh := range b.sheets {
		v, err := b.view(sh, anchors[sh])
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (b *Book) view(sh string, anchors []anchor) (SheetView, error) {
	f := b.f
	v := SheetView{Name: sh, Cells: []CellView{}, Styles: []CellStyle{}, Pictures: []PictureView{}}
	maxRow, maxCol := 1, 1
	grow := func(cell string) {
		if c, r, err := excelize.CellNameToCoordinates(cell); err == nil {
			maxRow, maxCol = max(maxRow, r), max(maxCol, c)
		}
	}
	if dim, err := f.GetSheetDimension(sh); err == nil {
		for _, p := range strings.Split(dim, ":") {
			grow(p)
		}
	}
	merges, err := f.GetMergeCells(sh)
	if err != nil {
		return v, err
	}
	span := map[string][2]int{}
	coveredBy := map[string]bool{}
	for _, m := range merges {
		grow(m.GetEndAxis())
		c1, r1, _ := excelize.CellNameToCoordinates(m.GetStartAxis())
		c2, r2, _ := excelize.CellNameToCoordinates(m.GetEndAxis())
		span[m.GetStartAxis()] = [2]int{c2 - c1 + 1, r2 - r1 + 1}
		for r := r1; r <= r2; r++ {
			for c := c1; c <= c2; c++ {
				if c != c1 || r != r1 {
					name, _ := excelize.CoordinatesToCellName(c, r)
					coveredBy[name] = true
				}
			}
		}
	}
	for cell := range b.fields[sh] {
		grow(cell)
	}

	for c := 1; c <= maxCol; c++ {
		name, _ := excelize.ColumnNumberToName(c)
		w, err := f.GetColWidth(sh, name)
		if err != nil {
			return v, err
		}
		px := 0
		if w > 0 {
			px = int(math.Round(w*7 + 5))
		}
		v.Cols = append(v.Cols, px)
	}
	for r := 1; r <= maxRow; r++ {
		h, err := f.GetRowHeight(sh, r)
		if err != nil {
			return v, err
		}
		v.Rows = append(v.Rows, int(math.Round(h*4/3)))
	}

	styleIndex := map[int]int{}
	for r := 1; r <= maxRow; r++ {
		for c := 1; c <= maxCol; c++ {
			ref, _ := excelize.CoordinatesToCellName(c, r)
			if coveredBy[ref] {
				continue
			}
			val, _ := f.GetCellValue(sh, ref)
			sid, _ := f.GetCellStyle(sh, ref)
			fillable := b.fields[sh][ref]
			sp, merged := span[ref]
			if val == "" && sid == 0 && !fillable && !merged {
				continue
			}
			idx, ok := styleIndex[sid]
			if !ok {
				st, err := f.GetStyle(sid)
				if err != nil {
					return v, err
				}
				idx = len(v.Styles)
				styleIndex[sid] = idx
				v.Styles = append(v.Styles, cellStyle(st))
			}
			cv := CellView{Ref: ref, Row: r, Col: c, Value: val, Style: idx, Fillable: fillable}
			if fillable {
				cv.Auto = b.auto[sh][ref]
			}
			if merged {
				cv.ColSpan, cv.RowSpan = sp[0], sp[1]
			}
			v.Cells = append(v.Cells, cv)
		}
	}

	// Logos (the pictures still in the sheet: tables redrawn as cells were removed).
	cells, _ := f.GetPictureCells(sh)
	present := map[string]bool{}
	for _, c := range cells {
		present[c] = true
	}
	for _, a := range anchors {
		ref, _ := excelize.CoordinatesToCellName(a.col+1, a.row+1)
		if !present[ref] || a.src == "" {
			continue
		}
		v.Pictures = append(v.Pictures, PictureView{Row: a.row + 1, Col: a.col + 1, DX: a.dx, DY: a.dy, Width: a.w, Height: a.h, Src: a.src, Crop: a.crop})
	}
	return v, nil
}

var themeColors = map[int]string{0: "#FFFFFF", 1: "#000000", 2: "#E7E6E6", 3: "#44546A", 4: "#4472C4", 5: "#ED7D31"}

func hexColor(c string) string {
	c = strings.TrimPrefix(strings.ToUpper(c), "#")
	if len(c) == 8 {
		c = c[2:]
	}
	if len(c) != 6 {
		return ""
	}
	return "#" + c
}

func cellStyle(st *excelize.Style) CellStyle {
	var s CellStyle
	if st.Font != nil {
		s.Bold, s.Italic, s.Underline = st.Font.Bold, st.Font.Italic, st.Font.Underline != ""
		s.Size, s.Font = st.Font.Size, st.Font.Family
		switch {
		case st.Font.Color != "":
			s.Color = hexColor(st.Font.Color)
		case st.Font.ColorTheme != nil:
			s.Color = themeColors[*st.Font.ColorTheme]
		}
	}
	if st.Fill.Type == "pattern" && st.Fill.Pattern == 1 && len(st.Fill.Color) > 0 {
		s.Fill = hexColor(st.Fill.Color[0])
	}
	if st.Alignment != nil {
		s.HAlign, s.VAlign, s.Wrap = st.Alignment.Horizontal, st.Alignment.Vertical, st.Alignment.WrapText
	}
	for _, bd := range st.Border {
		w := 1
		switch bd.Style {
		case 0:
			w = 0
		case 2, 8, 10, 12:
			w = 2
		case 5, 6:
			w = 3
		}
		switch bd.Type {
		case "left":
			s.Borders[0] = w
		case "right":
			s.Borders[1] = w
		case "top":
			s.Borders[2] = w
		case "bottom":
			s.Borders[3] = w
		}
	}
	return s
}

// anchor is a picture of the template: its cell (0-based), offset and size in pixels.
type anchor struct {
	col, row, dx, dy, w, h int
	src                    string // data: URL; empty for formats a browser can't show (EMF)
	crop                   [4]float64
}

const emuPerPixel = 9525

// templateAnchors reads where each picture sits in the template, by sheet name: excelize gives the
// pictures but not their drawn size.
func templateAnchors(tpl []byte) map[string][]anchor {
	out := map[string][]anchor{}
	zr, err := zip.NewReader(bytes.NewReader(tpl), int64(len(tpl)))
	if err != nil {
		return out
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) []byte {
		f, ok := files[name]
		if !ok {
			return nil
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		defer rc.Close()
		data, _ := io.ReadAll(rc)
		return data
	}
	type rel struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	}
	rels := func(name string) map[string]string {
		var r struct {
			Rels []rel `xml:"Relationship"`
		}
		_ = xml.Unmarshal(read(name), &r)
		m := map[string]string{}
		dir := path.Dir(strings.Replace(name, "_rels/", "", 1))
		for _, x := range r.Rels {
			m[x.ID] = path.Clean(path.Join(dir, x.Target))
		}
		return m
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if xml.Unmarshal(read("xl/workbook.xml"), &wb) != nil {
		return out
	}
	wbRels := rels("xl/_rels/workbook.xml.rels")
	for _, s := range wb.Sheets {
		sheetPath := wbRels[s.RID]
		sheetRels := rels(path.Join(path.Dir(sheetPath), "_rels", path.Base(sheetPath)+".rels"))
		for _, target := range sheetRels {
			if !strings.Contains(target, "/drawings/drawing") || !strings.HasSuffix(target, ".xml") {
				continue
			}
			drawRels := rels(path.Join(path.Dir(target), "_rels", path.Base(target)+".rels"))
			out[s.Name] = append(out[s.Name], parseAnchors(read(target), drawRels, read)...)
		}
	}
	return out
}

func parseAnchors(data []byte, rels map[string]string, read func(string) []byte) []anchor {
	type marker struct {
		Col    int `xml:"col"`
		ColOff int `xml:"colOff"`
		Row    int `xml:"row"`
		RowOff int `xml:"rowOff"`
	}
	type anc struct {
		From marker `xml:"from"`
		Ext  struct {
			Cx int `xml:"cx,attr"`
			Cy int `xml:"cy,attr"`
		} `xml:"ext"`
		Pic struct {
			Blip struct {
				Embed string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships embed,attr"`
			} `xml:"blipFill>blip"`
			// In 1/1000 of a percent: l="22339" cuts 22.339 % on the left.
			Crop struct {
				L int `xml:"l,attr"`
				T int `xml:"t,attr"`
				R int `xml:"r,attr"`
				B int `xml:"b,attr"`
			} `xml:"blipFill>srcRect"`
		} `xml:"pic"`
	}
	var d struct {
		One []anc `xml:"oneCellAnchor"`
		Two []anc `xml:"twoCellAnchor"`
	}
	if xml.Unmarshal(data, &d) != nil {
		return nil
	}
	var out []anchor
	for _, a := range append(d.One, d.Two...) {
		x := anchor{col: a.From.Col, row: a.From.Row, dx: a.From.ColOff / emuPerPixel, dy: a.From.RowOff / emuPerPixel,
			w: a.Ext.Cx / emuPerPixel, h: a.Ext.Cy / emuPerPixel,
			crop: [4]float64{float64(a.Pic.Crop.L) / 1e5, float64(a.Pic.Crop.T) / 1e5, float64(a.Pic.Crop.R) / 1e5, float64(a.Pic.Crop.B) / 1e5}}
		media := rels[a.Pic.Blip.Embed]
		mime := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif"}[strings.ToLower(path.Ext(media))]
		if mime != "" && x.w > 0 {
			x.src = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(read(media))
		}
		out = append(out, x)
	}
	return out
}

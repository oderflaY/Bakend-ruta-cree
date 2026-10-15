package formatos

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"regexp"
)

var legacyDrawing = regexp.MustCompile(`<legacyDrawing [^>]*(/>|>\s*</legacyDrawing>)`)

// dropLegacyDrawings removes the VML pictures of the given sheets. Hoja2 of the log shows the users
// table twice as an Excel "camera" picture of Hoja3 (a live image of A2:H7); here each table is
// redrawn as cells with its own route's data, so the picture would cover them.
func dropLegacyDrawings(data []byte, sheets map[string]bool) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	read := func(name string) []byte {
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil
				}
				defer rc.Close()
				b, _ := io.ReadAll(rc)
				return b
			}
		}
		return nil
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(read("xl/workbook.xml"), &wb) != nil || xml.Unmarshal(read("xl/_rels/workbook.xml.rels"), &rels) != nil {
		return data, nil
	}
	targets := map[string]bool{}
	for _, s := range wb.Sheets {
		if !sheets[s.Name] {
			continue
		}
		for _, r := range rels.Rels {
			if r.ID == s.RID {
				t := r.Target
				if path.IsAbs(t) {
					t = t[1:]
				} else {
					t = path.Join("xl", t)
				}
				targets[path.Clean(t)] = true
			}
		}
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: f.Modified})
		if err != nil {
			return nil, err
		}
		body := read(f.Name)
		if targets[f.Name] {
			body = legacyDrawing.ReplaceAll(body, nil)
		}
		if _, err := w.Write(body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

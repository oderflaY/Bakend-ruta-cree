package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/unicode/norm"

	"github.com/eter/rutacree-backend/internal/dispatch"
	"github.com/eter/rutacree-backend/internal/httpx"
)

// FleetBus is one unit of the DIF fleet spreadsheet ("PARQUE VEHICULAR").
type FleetBus struct {
	Number string `json:"number"` // No. económico, e.g. 25-3369
	Plates string `json:"plates"`
	Serial string `json:"serial"`
	UnitNo string `json:"unitNo"` // No. de camión painted on the unit ("" when N/A)
	Brand  string `json:"brand"`
	Model  string `json:"model"`
	Year   *int   `json:"year"`
	Centro string `json:"centro"`
}

// The first block of the sheet has no title: those are the CREE Durango units (the Ruta Azul).
const firstCentro = "CREE DURANGO"

// Typos in the DIF sheet, fixed so the printed forms read right.
var brandFix = map[string]string{"VOLSKWAGEN": "VOLKSWAGEN", "VOLKSWAGUEN": "VOLKSWAGEN", "MERCEDEZ": "MERCEDES"}

func fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ParseFleet reads the fleet workbook: blocks of units, each under a title row with its centre
// ("CREE GOMEZ PALACIO", "C.R.E.E. PUEBLO NUEVO", …) and a header row (# ECONOMICO, #PLACA, #SERIE,
// No. CAMION, MODELO, AÑO). Coordinator lines and blank rows are skipped.
func ParseFleet(r io.Reader) ([]FleetBus, error) {
	f, err := excelize.OpenReader(r)
	if err != nil {
		return nil, httpx.BadRequest("No se pudo leer el Excel: " + err.Error())
	}
	defer f.Close()
	rows, err := f.GetRows(f.GetSheetName(0))
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	centro := firstCentro
	var out []FleetBus
	seen := map[string]bool{}
	cell := func(row []string, k string) string {
		i, ok := col[k]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	for n, row := range rows {
		var filled []string
		for _, c := range row {
			if c = strings.TrimSpace(c); c != "" {
				filled = append(filled, c)
			}
		}
		if len(filled) == 0 {
			continue
		}
		// Header row: remember where each column is.
		if hdr := headerColumns(row); len(hdr) >= 3 {
			col = hdr
			continue
		}
		if len(filled) == 1 { // block title (centre) or its coordinator
			if t := fold(filled[0]); !strings.HasPrefix(t, "COORDINADOR") {
				centro = strings.TrimSuffix(t, ".")
			}
			continue
		}
		if len(col) == 0 {
			continue
		}
		num := strings.ToUpper(strings.ReplaceAll(cell(row, "number"), " ", ""))
		if num == "" {
			continue
		}
		if !dispatch.BusNumberPattern.MatchString(num) {
			return nil, httpx.BadRequest(fmt.Sprintf("Renglón %d: número económico inválido %q", n+1, num))
		}
		if seen[num] {
			return nil, httpx.BadRequest(fmt.Sprintf("Renglón %d: la unidad %s aparece dos veces", n+1, num))
		}
		seen[num] = true
		b := FleetBus{Number: num, Centro: centro}
		if b.Plates, err = cleanPlates(cell(row, "plates")); err != nil {
			return nil, httpx.BadRequest(fmt.Sprintf("Renglón %d: placas inválidas %q", n+1, cell(row, "plates")))
		}
		b.Serial = strings.ToUpper(strings.ReplaceAll(cell(row, "serial"), " ", ""))
		if !serialPattern.MatchString(b.Serial) {
			return nil, httpx.BadRequest(fmt.Sprintf("Renglón %d: serie inválida %q", n+1, b.Serial))
		}
		if u := strings.ToUpper(cell(row, "unit")); u != "N/A" && u != "NA" && u != "-" {
			b.UnitNo = u
		}
		if utf8.RuneCountInString(b.UnitNo) > 8 {
			return nil, httpx.BadRequest(fmt.Sprintf("Renglón %d: No. de camión demasiado largo", n+1))
		}
		// MODELO holds "MARCA MODELO" (VOLKSWAGEN CRAFTER) or only the brand (DINA).
		words := strings.Fields(fold(cell(row, "model")))
		if len(words) > 0 {
			if fix, ok := brandFix[words[0]]; ok {
				words[0] = fix
			}
			b.Brand, b.Model = words[0], strings.Join(words[1:], " ")
		}
		if y, err := strconv.Atoi(cell(row, "year")); err == nil && y > 1950 && y < 2100 {
			b.Year = &y
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, httpx.BadRequest("El Excel no trae unidades (falta el renglón de títulos # ECONOMICO, #PLACA…)")
	}
	return out, nil
}

func headerColumns(row []string) map[string]int {
	m := map[string]int{}
	for i, c := range row {
		switch t := strings.ReplaceAll(fold(c), " ", ""); {
		case strings.Contains(t, "ECONOMICO"):
			m["number"] = i
		case strings.Contains(t, "PLACA"):
			m["plates"] = i
		case strings.Contains(t, "SERIE"):
			m["serial"] = i
		case strings.Contains(t, "CAMION"):
			m["unit"] = i
		case strings.Contains(t, "MODELO") || strings.Contains(t, "MARCA"):
			m["model"] = i
		case t == "ANO" || t == "AÑO":
			m["year"] = i
		}
	}
	if _, ok := m["number"]; !ok {
		return nil
	}
	return m
}

// FleetResult says what an import changed.
type FleetResult struct {
	Created []string `json:"created"`
	Updated []string `json:"updated"`
	Retired []string `json:"retired"`
	retired []uint64
}

// ImportFleet upserts the units by No. económico (a unit dada de baja that shows up again comes back
// as active). With retireOthers, every other unit is marked "retired": test buses and units that left
// the fleet stop showing in the panel's pickers, but their history stays.
func ImportFleet(ctx context.Context, db *sql.DB, fleet []FleetBus, retireOthers bool) (FleetResult, error) {
	var res FleetResult
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, b := range fleet {
		keep[b.Number] = true
		var id uint64
		err := tx.QueryRowContext(ctx, `SELECT id FROM buses WHERE number = ? FOR UPDATE`, b.Number).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, `INSERT INTO buses (number, plates, brand, model, serial, year, unit_no, centro)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, b.Number, b.Plates, b.Brand, b.Model, b.Serial, b.Year, b.UnitNo, b.Centro); err != nil {
				return res, err
			}
			res.Created = append(res.Created, b.Number)
		case err != nil:
			return res, err
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE buses SET plates = ?, brand = ?, model = ?, serial = ?, year = ?, unit_no = ?, centro = ?,
				status = IF(status = 'retired', 'active', status), ramp_operational = (status <> 'ramp_broken')
				WHERE id = ?`, b.Plates, b.Brand, b.Model, b.Serial, b.Year, b.UnitNo, b.Centro, id); err != nil {
				return res, err
			}
			res.Updated = append(res.Updated, b.Number)
		}
	}
	if retireOthers {
		rows, err := tx.QueryContext(ctx, `SELECT id, number FROM buses WHERE status <> 'retired'`)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var id uint64
			var num string
			if err := rows.Scan(&id, &num); err != nil {
				rows.Close()
				return res, err
			}
			if !keep[num] {
				res.retired = append(res.retired, id)
				res.Retired = append(res.Retired, num)
			}
		}
		rows.Close()
		for _, id := range res.retired {
			if _, err := tx.ExecContext(ctx, `UPDATE buses SET status = 'retired' WHERE id = ?`, id); err != nil {
				return res, err
			}
		}
	}
	return res, tx.Commit()
}

// ImportBuses is POST /api/v1/admin/buses/import (multipart: "file" = the fleet .xlsx; "retireOthers"=1
// to dar de baja every unit missing from it). "dryRun"=1 only parses and returns the units.
func (h *Handler) ImportBuses(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		return httpx.BadRequest("Adjunta el Excel del parque vehicular")
	}
	defer file.Close()
	fleet, err := ParseFleet(file)
	if err != nil {
		return err
	}
	if r.FormValue("dryRun") == "1" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"units": fleet})
		return nil
	}
	res, err := ImportFleet(r.Context(), h.DB, fleet, r.FormValue("retireOthers") == "1")
	if err != nil {
		return err
	}
	for _, id := range res.retired {
		if err := h.Dispatch.CloseBusShifts(r.Context(), id); err != nil {
			return err
		}
	}
	h.audit(r, "importó el parque vehicular: %d nuevas, %d actualizadas, %d dadas de baja", len(res.Created), len(res.Updated), len(res.Retired))
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

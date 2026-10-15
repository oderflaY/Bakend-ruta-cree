package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/eter/rutacree-backend/internal/formatos"
	"github.com/eter/rutacree-backend/internal/httpx"
)

// The DIF's Excel forms, filled with the system's data and editable cell by cell from the panel:
// the weekly log (bitácora), the service order of a maintenance order and the vehicle report.
// Dates on the forms are local (Handler.Location).
//
//	GET  /api/v1/admin/formatos/{kind}/{ref}       the sheets as the panel draws them
//	PUT  /api/v1/admin/formatos/{kind}/{ref}       {"values": {"HOJA": {"D8": "07:30"}}} saves what was typed
//	GET  /api/v1/admin/formatos/{kind}/{ref}/xlsx  the Excel file
//
// kind is bitacora (ref: any day of the week, YYYY-MM-DD), orden (ref: maintenance order id) or
// vehiculo (ref: vehicle report id).

const xlsxType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func writeXLSX(w http.ResponseWriter, name string, data []byte) {
	w.Header().Set("Content-Type", xlsxType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (h *Handler) loc() *time.Location {
	if h.Location != nil {
		return h.Location
	}
	return time.Local
}

func nullInt(n sql.NullInt64) int {
	if n.Valid {
		return int(n.Int64)
	}
	return 0
}

// formRoles: who can open each form, like the screens they come from.
var formRoles = map[string][]Role{
	"bitacora": {SuperAdmin, Dispatcher},
	"orden":    {SuperAdmin, Mechanic},
	"vehiculo": {SuperAdmin, Dispatcher, Mechanic},
}

// form is one form instance: how to build it and how to name its file.
type form struct {
	kind, ref, title, file string
	build                  func(formatos.Values) (*formatos.Book, error)
}

func (h *Handler) form(r *http.Request) (form, error) {
	kind, ref := r.PathValue("kind"), r.PathValue("ref")
	roles, ok := formRoles[kind]
	if !ok {
		return form{}, httpx.ErrNotFound
	}
	allowed := false
	for _, role := range roles {
		allowed = allowed || current(r).Role == role
	}
	if !allowed {
		return form{}, httpx.ErrForbidden
	}
	ctx := r.Context()
	switch kind {
	case "bitacora":
		day, err := time.ParseInLocation(time.DateOnly, ref, h.loc())
		if err != nil {
			return form{}, httpx.BadRequest("La semana va como fecha YYYY-MM-DD")
		}
		monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
		week, err := h.week(ctx, monday)
		if err != nil {
			return form{}, err
		}
		friday := monday.AddDate(0, 0, 4)
		return form{kind: kind, ref: monday.Format(time.DateOnly),
			title: fmt.Sprintf("Bitácora semanal del %d/%02d al %d/%02d/%d", monday.Day(), monday.Month(), friday.Day(), friday.Month(), friday.Year()),
			file:  "bitacora-" + monday.Format(time.DateOnly) + ".xlsx",
			build: func(v formatos.Values) (*formatos.Book, error) { return formatos.Bitacora(week, v) }}, nil
	case "orden":
		id, err := strconv.ParseUint(ref, 10, 64)
		if err != nil {
			return form{}, httpx.ErrNotFound
		}
		o, err := h.loadMaintenance(ctx, id)
		if err != nil {
			return form{}, err
		}
		bus, err := h.formBus(ctx, o.BusID)
		if err != nil {
			return form{}, err
		}
		items := make([]formatos.ServiceItem, len(o.Items))
		for i, it := range o.Items {
			items[i] = formatos.ServiceItem{Description: it.Description, Unit: it.Unit, Quantity: it.Quantity}
		}
		so := formatos.ServiceOrder{Date: o.OpenedAt.In(h.loc()), Km: o.Km, Partida: o.Partida, Recurso: o.Recurso,
			Execution: o.Execution, Bus: bus, Items: items}
		return form{kind: kind, ref: ref, title: fmt.Sprintf("Orden de servicio · Unidad %s", bus.Number),
			file:  fmt.Sprintf("orden-servicio-%d-unidad-%s.xlsx", o.ID, bus.Number),
			build: func(v formatos.Values) (*formatos.Book, error) { return formatos.Orden(so, v) }}, nil
	default: // vehiculo
		id, err := strconv.ParseUint(ref, 10, 64)
		if err != nil {
			return form{}, httpx.ErrNotFound
		}
		vr, err := scanVehicleReport(h.DB.QueryRowContext(ctx, vehicleReportQuery+` WHERE v.id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return form{}, httpx.ErrNotFound
		}
		if err != nil {
			return form{}, err
		}
		bus, err := h.formBus(ctx, vr.BusID)
		if err != nil {
			return form{}, err
		}
		day, _ := time.Parse(time.DateOnly, vr.Day)
		rep := formatos.VehicleReport{Date: day, Bus: bus, Observations: vr.Observations}
		return form{kind: kind, ref: ref, title: fmt.Sprintf("Reporte de vehículo · Unidad %s · %s", bus.Number, vr.Day),
			file:  fmt.Sprintf("reporte-vehiculo-%s-unidad-%s.xlsx", vr.Day, bus.Number),
			build: func(v formatos.Values) (*formatos.Book, error) { return formatos.Vehiculo(rep, v) }}, nil
	}
}

// week gathers the trips and the drivers' failure reports of the week starting on monday.
func (h *Handler) week(ctx context.Context, monday time.Time) (formatos.Week, error) {
	w := formatos.Week{Monday: monday, Routes: h.routeInfo()}
	var err error
	if w.Trips, err = h.trips(ctx, monday, monday.AddDate(0, 0, 7)); err != nil {
		return w, err
	}
	w.Notes, w.Incidents, err = h.incidents(ctx, monday, monday.AddDate(0, 0, 7))
	return w, err
}

// trips are the routes started from `from` until `to` (local times), with the users the drivers counted.
func (h *Handler) trips(ctx context.Context, from, to time.Time) ([]formatos.Trip, error) {
	loc := h.loc()
	rows, err := h.DB.QueryContext(ctx, `SELECT s.route_id, d.name, s.started_at, s.ended_at,
		s.disability, s.elderly, s.pregnant, s.companions, s.men, s.women
		FROM shifts s JOIN drivers d ON d.id = s.driver_id
		WHERE s.started_at >= ? AND s.started_at < ? ORDER BY s.started_at`, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []formatos.Trip
	for rows.Next() {
		var (
			t                             formatos.Trip
			ended                         sql.NullTime
			dis, eld, preg, comp, men, wo sql.NullInt64
		)
		if err := rows.Scan(&t.RouteID, &t.Driver, &t.Started, &ended, &dis, &eld, &preg, &comp, &men, &wo); err != nil {
			return nil, err
		}
		t.Started = t.Started.In(loc)
		if ended.Valid {
			e := ended.Time.In(loc)
			t.Ended = &e
		}
		t.Users = formatos.Counts{Disability: nullInt(dis), Elderly: nullInt(eld), Pregnant: nullInt(preg),
			Companions: nullInt(comp), Men: nullInt(men), Women: nullInt(wo)}
		t.Counted = men.Valid || wo.Valid
		out = append(out, t)
	}
	return out, rows.Err()
}

// incidents are the drivers' failure reports from `from` until `to`: as notes for OBSERVACIONES and as
// counts for the statistics.
func (h *Handler) incidents(ctx context.Context, from, to time.Time) ([]formatos.Note, []formatos.IncidentAt, error) {
	loc := h.loc()
	rows, err := h.DB.QueryContext(ctx, `SELECT i.route_id, i.type, i.created_at, b.number
		FROM incidents i JOIN buses b ON b.id = i.bus_id
		WHERE i.created_at >= ? AND i.created_at < ? ORDER BY i.created_at`, from.UTC(), to.UTC())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	label := map[string]string{"accident": "Accidente", "breakdown": "Falla del camión", "ramp_failure": "Falla de rampa"}
	var notes []formatos.Note
	var all []formatos.IncidentAt
	for rows.Next() {
		var n formatos.Note
		var typ, bus string
		if err := rows.Scan(&n.RouteID, &typ, &n.At, &bus); err != nil {
			return nil, nil, err
		}
		n.At = n.At.In(loc)
		n.Text = fmt.Sprintf("%s (Unidad %s)", label[typ], bus)
		notes = append(notes, n)
		all = append(all, formatos.IncidentAt{Kind: typ, At: n.At})
	}
	return notes, all, rows.Err()
}

func (h *Handler) routeInfo() map[string]formatos.RouteInfo {
	out := map[string]formatos.RouteInfo{}
	if h.Catalog == nil {
		return out
	}
	for _, r := range h.Catalog.Routes() {
		out[r.ID] = formatos.RouteInfo{Name: r.Name, Color: r.Color}
	}
	return out
}

// statsPeriod reads ?desde=YYYY-MM-DD&hasta=YYYY-MM-DD (local days, inclusive; by default the last 30
// days) and answers the period's statistics.
func (h *Handler) statsPeriod(r *http.Request) (formatos.Stats, error) {
	loc := h.loc()
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from, to := today.AddDate(0, 0, -29), today
	parse := func(key string, def time.Time) (time.Time, error) {
		v := r.URL.Query().Get(key)
		if v == "" {
			return def, nil
		}
		t, err := time.ParseInLocation(time.DateOnly, v, loc)
		if err != nil {
			return t, httpx.BadRequest(key + " va como fecha YYYY-MM-DD")
		}
		return t, nil
	}
	var err error
	if from, err = parse("desde", from); err != nil {
		return formatos.Stats{}, err
	}
	if to, err = parse("hasta", to); err != nil {
		return formatos.Stats{}, err
	}
	if to.Before(from) {
		return formatos.Stats{}, httpx.BadRequest("hasta no puede ser antes de desde")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return formatos.Stats{}, httpx.BadRequest("El periodo va hasta de un año")
	}
	end := to.AddDate(0, 0, 1)
	trips, err := h.trips(r.Context(), from, end)
	if err != nil {
		return formatos.Stats{}, err
	}
	_, inc, err := h.incidents(r.Context(), from, end)
	if err != nil {
		return formatos.Stats{}, err
	}
	return formatos.Summarize(from, to, trips, inc, h.routeInfo()), nil
}

// Estadisticas is GET /api/v1/admin/estadisticas?desde=YYYY-MM-DD&hasta=YYYY-MM-DD.
func (h *Handler) Estadisticas(w http.ResponseWriter, r *http.Request) error {
	s, err := h.statsPeriod(r)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, s)
	return nil
}

// EstadisticasXLSX is GET /api/v1/admin/estadisticas/xlsx?desde=…&hasta=…: the same, as an Excel
// workbook with tables and charts.
func (h *Handler) EstadisticasXLSX(w http.ResponseWriter, r *http.Request) error {
	s, err := h.statsPeriod(r)
	if err != nil {
		return err
	}
	data, err := formatos.StatsWorkbook(s)
	if err != nil {
		return err
	}
	writeXLSX(w, fmt.Sprintf("estadisticas-%s-al-%s.xlsx", s.From, s.To), data)
	return nil
}

func (h *Handler) formValues(ctx context.Context, kind, ref string) (formatos.Values, error) {
	var raw []byte
	err := h.DB.QueryRowContext(ctx, `SELECT data FROM form_values WHERE kind = ? AND ref = ?`, kind, ref).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return formatos.Values{}, nil
	}
	if err != nil {
		return nil, err
	}
	v := formatos.Values{}
	_ = json.Unmarshal(raw, &v)
	return v, nil
}

type formView struct {
	Kind   string               `json:"kind"`
	Ref    string               `json:"ref"`
	Title  string               `json:"title"`
	Sheets []formatos.SheetView `json:"sheets"`
	// Edited are the cells typed in the panel (they differ from what the system fills in).
	Edited formatos.Values `json:"edited"`
}

func (h *Handler) viewForm(w http.ResponseWriter, f form, values formatos.Values) error {
	b, err := f.build(values)
	if err != nil {
		return err
	}
	defer b.Close()
	sheets, err := b.Views()
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, formView{Kind: f.kind, Ref: f.ref, Title: f.title, Sheets: sheets, Edited: values})
	return nil
}

// GetForm is GET /api/v1/admin/formatos/{kind}/{ref}.
func (h *Handler) GetForm(w http.ResponseWriter, r *http.Request) error {
	f, err := h.form(r)
	if err != nil {
		return err
	}
	values, err := h.formValues(r.Context(), f.kind, f.ref)
	if err != nil {
		return err
	}
	return h.viewForm(w, f, values)
}

// SaveForm is PUT /api/v1/admin/formatos/{kind}/{ref} {"values": {"HOJA": {"D8": "07:30"}}}: the whole
// set of typed cells (a cell left as the system filled it follows the system again).
func (h *Handler) SaveForm(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Values formatos.Values `json:"values"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	f, err := h.form(r)
	if err != nil {
		return err
	}
	auto, err := f.build(nil)
	if err != nil {
		return err
	}
	clean := auto.Clean(req.Values)
	auto.Close()
	raw, _ := json.Marshal(clean)
	if len(raw) > 1<<20 {
		return httpx.BadRequest("Demasiado texto en el formato")
	}
	if _, err := h.DB.ExecContext(r.Context(), `INSERT INTO form_values (kind, ref, data, updated_by) VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE data = VALUES(data), updated_by = VALUES(updated_by)`, f.kind, f.ref, string(raw), current(r).ID); err != nil {
		return err
	}
	n := 0
	for _, cells := range clean {
		n += len(cells)
	}
	h.audit(r, "editó el formato %s (%d celdas escritas a mano)", f.title, n)
	return h.viewForm(w, f, clean)
}

// FormXLSX is GET /api/v1/admin/formatos/{kind}/{ref}/xlsx.
func (h *Handler) FormXLSX(w http.ResponseWriter, r *http.Request) error {
	f, err := h.form(r)
	if err != nil {
		return err
	}
	values, err := h.formValues(r.Context(), f.kind, f.ref)
	if err != nil {
		return err
	}
	b, err := f.build(values)
	if err != nil {
		return err
	}
	defer b.Close()
	data, err := b.XLSX()
	if err != nil {
		return err
	}
	writeXLSX(w, f.file, data)
	return nil
}

// Older download links of the panel, now served by the same forms.

// Bitacora is GET /api/v1/admin/formatos/bitacora?semana=YYYY-MM-DD.
func (h *Handler) Bitacora(w http.ResponseWriter, r *http.Request) error {
	r.SetPathValue("kind", "bitacora")
	r.SetPathValue("ref", r.URL.Query().Get("semana"))
	return h.FormXLSX(w, r)
}

// OrdenServicio is GET /api/v1/admin/maintenance/{id}/orden.
func (h *Handler) OrdenServicio(w http.ResponseWriter, r *http.Request) error {
	r.SetPathValue("kind", "orden")
	r.SetPathValue("ref", r.PathValue("id"))
	return h.FormXLSX(w, r)
}

// VehicleReportXLSX is GET /api/v1/admin/vehicle-reports/{id}/xlsx.
func (h *Handler) VehicleReportXLSX(w http.ResponseWriter, r *http.Request) error {
	r.SetPathValue("kind", "vehiculo")
	r.SetPathValue("ref", r.PathValue("id"))
	return h.FormXLSX(w, r)
}

func (h *Handler) formBus(ctx context.Context, id uint64) (formatos.Bus, error) {
	b, err := h.loadBus(ctx, id)
	if err != nil {
		return formatos.Bus{}, err
	}
	return formatos.Bus{Number: b.Number, Plates: b.Plates, Brand: b.Brand, Model: b.Model}, nil
}

// ---- vehicle reports ----

type vehicleReport struct {
	ID           uint64    `json:"id"`
	BusID        uint64    `json:"busId"`
	BusNumber    string    `json:"busNumber"`
	Day          string    `json:"day"`
	Observations string    `json:"observations"`
	CreatedBy    *string   `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

const vehicleReportQuery = `SELECT v.id, v.bus_id, b.number, DATE_FORMAT(v.day, '%Y-%m-%d'), v.observations, a.username, v.created_at
	FROM vehicle_reports v JOIN buses b ON b.id = v.bus_id LEFT JOIN admins a ON a.id = v.created_by`

func scanVehicleReport(sc interface{ Scan(...any) error }) (vehicleReport, error) {
	var v vehicleReport
	err := sc.Scan(&v.ID, &v.BusID, &v.BusNumber, &v.Day, &v.Observations, &v.CreatedBy, &v.CreatedAt)
	return v, err
}

// ListVehicleReports is GET /api/v1/admin/vehicle-reports?busId=N&limit=N (newest first).
func (h *Handler) ListVehicleReports(w http.ResponseWriter, r *http.Request) error {
	q, args := vehicleReportQuery, []any{}
	if v := r.URL.Query().Get("busId"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return httpx.BadRequest("busId inválido")
		}
		q, args = q+` WHERE v.bus_id = ?`, append(args, id)
	}
	rows, err := h.DB.QueryContext(r.Context(), q+` ORDER BY v.day DESC, v.id DESC LIMIT ?`, append(args, limitParam(r))...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []vehicleReport{}
	for rows.Next() {
		v, err := scanVehicleReport(rows)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// CreateVehicleReport is POST /api/v1/admin/vehicle-reports {"busId", "day": "YYYY-MM-DD", "observations"?}.
// The observations can also be written later, on the form itself.
func (h *Handler) CreateVehicleReport(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		BusID        uint64 `json:"busId"`
		Day          string `json:"day"`
		Observations string `json:"observations"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if _, err := time.Parse(time.DateOnly, req.Day); err != nil {
		return httpx.BadRequest("day debe ser YYYY-MM-DD")
	}
	obs, err := cleanText(req.Observations, 2000, "Las observaciones")
	if err != nil {
		return err
	}
	b, err := h.loadBus(r.Context(), req.BusID)
	if errors.Is(err, httpx.ErrNotFound) {
		return httpx.BadRequest("Unidad desconocida")
	}
	if err != nil {
		return err
	}
	res, err := h.DB.ExecContext(r.Context(), `INSERT INTO vehicle_reports (bus_id, day, observations, created_by) VALUES (?, ?, ?, ?)`,
		b.ID, req.Day, obs, current(r).ID)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	h.audit(r, "levantó un reporte de vehículo de la Unidad %s (%s)", b.Number, req.Day)
	v, err := scanVehicleReport(h.DB.QueryRowContext(r.Context(), vehicleReportQuery+` WHERE v.id = ?`, id))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, v)
	return nil
}

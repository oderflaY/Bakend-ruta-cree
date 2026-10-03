package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/go-sql-driver/mysql"
)

// Scheduled shifts: who drives which bus on which route each day. The day is the calendar date in
// Durango as the dispatcher picks it; no time zone math happens here.

type assignment struct {
	ID       uint64 `json:"id"`
	Date     string `json:"date"`
	DriverID string `json:"driverId"`
	BusID    uint64 `json:"busId"`
	RouteID  string `json:"routeId"`
}

const assignmentQuery = `SELECT id, DATE_FORMAT(day, '%Y-%m-%d'), driver_id, bus_id, route_id FROM shift_assignments`

func scanAssignment(sc interface{ Scan(...any) error }) (assignment, error) {
	var a assignment
	err := sc.Scan(&a.ID, &a.Date, &a.DriverID, &a.BusID, &a.RouteID)
	return a, err
}

func dayParam(r *http.Request, key string, def time.Time) (string, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def.Format(time.DateOnly), nil
	}
	if _, err := time.Parse(time.DateOnly, v); err != nil {
		return "", httpx.BadRequest(key + " debe ser YYYY-MM-DD")
	}
	return v, nil
}

// ListAssignments is GET /api/v1/admin/assignments?from=YYYY-MM-DD&to=YYYY-MM-DD (default: a week back
// to two months ahead).
func (h *Handler) ListAssignments(w http.ResponseWriter, r *http.Request) error {
	now := time.Now()
	from, err := dayParam(r, "from", now.AddDate(0, 0, -7))
	if err != nil {
		return err
	}
	to, err := dayParam(r, "to", now.AddDate(0, 2, 0))
	if err != nil {
		return err
	}
	rows, err := h.DB.QueryContext(r.Context(), assignmentQuery+` WHERE day BETWEEN ? AND ? ORDER BY day, id LIMIT 5000`, from, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []assignment{}
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return err
		}
		out = append(out, a)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// CreateAssignment is POST /api/v1/admin/assignments {"date", "driverId", "busId", "routeId"}. A driver
// and a bus can each have one assignment per day; a bus in the workshop can't be assigned.
func (h *Handler) CreateAssignment(w http.ResponseWriter, r *http.Request) error {
	var req assignment
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if _, err := time.Parse(time.DateOnly, req.Date); err != nil {
		return httpx.BadRequest("date debe ser YYYY-MM-DD")
	}
	ctx := r.Context()
	var driverName string
	var driverActive bool
	err := h.DB.QueryRowContext(ctx, `SELECT name, active FROM drivers WHERE id = ?`, req.DriverID).Scan(&driverName, &driverActive)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !driverActive) {
		return httpx.BadRequest("Chofer desconocido o dado de baja")
	}
	if err != nil {
		return err
	}
	b, err := h.loadBus(ctx, req.BusID)
	if errors.Is(err, httpx.ErrNotFound) {
		return httpx.BadRequest("Unidad desconocida")
	}
	if err != nil {
		return err
	}
	if b.Status == "workshop" {
		return httpx.BadRequest("La Unidad " + b.Number + " está en el taller")
	}
	if _, ok := h.Catalog.Route(req.RouteID); !ok {
		return httpx.BadRequest("Ruta desconocida")
	}
	res, err := h.DB.ExecContext(ctx, `INSERT INTO shift_assignments (day, driver_id, bus_id, route_id, created_by) VALUES (?, ?, ?, ?, ?)`,
		req.Date, req.DriverID, req.BusID, req.RouteID, current(r).ID)
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok && me.Number == 1062 {
		return httpx.NewError(http.StatusConflict, "exists", "El chofer o la unidad ya tienen turno ese día")
	}
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	h.audit(r, "programó turno %s: %s en Unidad %s (%s)", req.Date, driverName, b.Number, req.RouteID)
	a, err := scanAssignment(h.DB.QueryRowContext(ctx, assignmentQuery+` WHERE id = ?`, id))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, a)
	return nil
}

// DeleteAssignment is DELETE /api/v1/admin/assignments/{id}.
func (h *Handler) DeleteAssignment(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	a, err := scanAssignment(h.DB.QueryRowContext(r.Context(), assignmentQuery+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := h.DB.ExecContext(r.Context(), `DELETE FROM shift_assignments WHERE id = ?`, id); err != nil {
		return err
	}
	h.audit(r, "canceló el turno del %s de %s", a.Date, a.DriverID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

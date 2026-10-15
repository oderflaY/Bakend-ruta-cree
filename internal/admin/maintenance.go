package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eter/rutacree-backend/internal/httpx"
)

// Maintenance orders. Opening one sends the bus to the workshop (off the map, its shift closed);
// closing it puts the bus back in service and records the maintenance date.

var maintenanceKinds = map[string]string{"preventive": "preventivo", "corrective": "correctivo", "ramp": "rampa"}

type maintenanceOrder struct {
	ID          uint64     `json:"id"`
	BusID       uint64     `json:"busId"`
	BusNumber   string     `json:"busNumber"`
	Kind        string     `json:"kind"`
	Description string     `json:"description"`
	Notes       string     `json:"notes"`
	OpenedBy    *string    `json:"openedBy"`
	OpenedAt    time.Time  `json:"openedAt"`
	ClosedBy    *string    `json:"closedBy"`
	ClosedAt    *time.Time `json:"closedAt"`
	// Service order (Solicitud y autorización del servicio) fields.
	Km        *int          `json:"km"`
	Partida   string        `json:"partida"`
	Recurso   string        `json:"recurso"`
	Execution string        `json:"execution"`
	Items     []serviceItem `json:"items"`
}

// serviceItem is one line of the service order: DESCRIPCIÓN DE LA SOLICITUD, UNIDAD, CANTIDAD.
type serviceItem struct {
	Description string `json:"description"`
	Unit        string `json:"unit"`
	Quantity    string `json:"quantity"`
}

// The printed form has room for four lines.
const maxServiceItems = 4

type serviceFields struct {
	Km        *int          `json:"km"`
	Partida   *string       `json:"partida"`
	Recurso   *string       `json:"recurso"`
	Execution *string       `json:"execution"`
	Items     []serviceItem `json:"items"`
}

// sets validates the service-order fields present in the request and returns the SQL to store them.
func (f serviceFields) sets() ([]string, []any, error) {
	sets, args := []string{}, []any{}
	if f.Km != nil {
		if *f.Km < 0 || *f.Km > 9_999_999 {
			return nil, nil, httpx.BadRequest("Kilometraje inválido")
		}
		sets, args = append(sets, "km = ?"), append(args, *f.Km)
	}
	for _, t := range []struct {
		v     *string
		col   string
		field string
	}{{f.Partida, "partida", "La partida"}, {f.Recurso, "recurso", "El recurso"}, {f.Execution, "execution", "La ejecución"}} {
		if t.v == nil {
			continue
		}
		v, err := cleanText(*t.v, 60, t.field)
		if err != nil {
			return nil, nil, err
		}
		sets, args = append(sets, t.col+" = ?"), append(args, v)
	}
	if f.Items != nil {
		if len(f.Items) > maxServiceItems {
			return nil, nil, httpx.BadRequest("La orden de servicio admite hasta 4 conceptos")
		}
		items := []serviceItem{}
		for _, it := range f.Items {
			d, err := cleanText(it.Description, 200, "La descripción del concepto")
			if err != nil {
				return nil, nil, err
			}
			u, err := cleanText(it.Unit, 20, "La unidad del concepto")
			if err != nil {
				return nil, nil, err
			}
			q, err := cleanText(it.Quantity, 20, "La cantidad del concepto")
			if err != nil {
				return nil, nil, err
			}
			if d != "" || u != "" || q != "" {
				items = append(items, serviceItem{d, u, q})
			}
		}
		raw, _ := json.Marshal(items)
		sets, args = append(sets, "items = ?"), append(args, string(raw))
	}
	return sets, args, nil
}

const maintenanceQuery = `SELECT m.id, m.bus_id, b.number, m.kind, m.description, m.notes,
	oa.username, m.opened_at, ca.username, m.closed_at, m.km, m.partida, m.recurso, m.execution, m.items
	FROM maintenance_orders m JOIN buses b ON b.id = m.bus_id
	LEFT JOIN admins oa ON oa.id = m.opened_by LEFT JOIN admins ca ON ca.id = m.closed_by`

func scanMaintenance(sc interface{ Scan(...any) error }) (maintenanceOrder, error) {
	var o maintenanceOrder
	var items []byte
	err := sc.Scan(&o.ID, &o.BusID, &o.BusNumber, &o.Kind, &o.Description, &o.Notes, &o.OpenedBy, &o.OpenedAt, &o.ClosedBy, &o.ClosedAt,
		&o.Km, &o.Partida, &o.Recurso, &o.Execution, &items)
	if err == nil {
		o.Items = []serviceItem{}
		_ = json.Unmarshal(items, &o.Items)
	}
	return o, err
}

func (h *Handler) loadMaintenance(ctx context.Context, id uint64) (maintenanceOrder, error) {
	o, err := scanMaintenance(h.DB.QueryRowContext(ctx, maintenanceQuery+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return o, httpx.ErrNotFound
	}
	return o, err
}

// setBusStatus changes a bus's status with its side effects: the ramp flag riders see, and closing
// the open shift of a bus sent to the workshop.
func (h *Handler) setBusStatus(ctx context.Context, id uint64, status BusStatus) error {
	if _, err := h.DB.ExecContext(ctx, `UPDATE buses SET status = ?, ramp_operational = ? WHERE id = ?`,
		status, status != "ramp_broken", id); err != nil {
		return err
	}
	h.Hub.SetRamp(id, status != "ramp_broken")
	if status == "workshop" {
		return h.Dispatch.CloseBusShifts(ctx, id)
	}
	return nil
}

// ListMaintenance is GET /api/v1/admin/maintenance?open=true|false&busId=N&limit=N (newest first).
func (h *Handler) ListMaintenance(w http.ResponseWriter, r *http.Request) error {
	q, args, where := maintenanceQuery, []any{}, []string{}
	switch r.URL.Query().Get("open") {
	case "true":
		where = append(where, "m.closed_at IS NULL")
	case "false":
		where = append(where, "m.closed_at IS NOT NULL")
	}
	if v := r.URL.Query().Get("busId"); v != "" {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return httpx.BadRequest("busId inválido")
		}
		where, args = append(where, "m.bus_id = ?"), append(args, id)
	}
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	rows, err := h.DB.QueryContext(r.Context(), q+` ORDER BY m.id DESC LIMIT ?`, append(args, limitParam(r))...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []maintenanceOrder{}
	for rows.Next() {
		o, err := scanMaintenance(rows)
		if err != nil {
			return err
		}
		out = append(out, o)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

func cleanText(s string, max int, field string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		return "", httpx.BadRequest(field + " demasiado largo")
	}
	return s, nil
}

// OpenMaintenance is POST /api/v1/admin/maintenance {"busId", "kind", "description", "km"?, "partida"?,
// "recurso"?, "execution"?, "items"?}: the last ones fill the printed service order.
func (h *Handler) OpenMaintenance(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		BusID       uint64 `json:"busId"`
		Kind        string `json:"kind"`
		Description string `json:"description"`
		serviceFields
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if _, ok := maintenanceKinds[req.Kind]; !ok {
		return httpx.BadRequest("kind debe ser preventive, corrective o ramp")
	}
	desc, err := cleanText(req.Description, 500, "La descripción")
	if err != nil {
		return err
	}
	if desc == "" {
		return httpx.BadRequest("Describe qué se le va a hacer a la unidad")
	}
	if req.Items == nil {
		// The request itself is the first line of the service order.
		req.Items = []serviceItem{{Description: desc, Unit: "SERVICIO", Quantity: "1"}}
	}
	sets, setArgs, err := req.serviceFields.sets()
	if err != nil {
		return err
	}
	ctx := r.Context()
	b, err := h.loadBus(ctx, req.BusID)
	if errors.Is(err, httpx.ErrNotFound) {
		return httpx.BadRequest("Unidad desconocida")
	}
	if err != nil {
		return err
	}
	var open int
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance_orders WHERE bus_id = ? AND closed_at IS NULL`, b.ID).Scan(&open); err != nil {
		return err
	}
	if open > 0 {
		return httpx.NewError(http.StatusConflict, "already_open", "La Unidad "+b.Number+" ya tiene una orden abierta")
	}
	res, err := h.DB.ExecContext(ctx, `INSERT INTO maintenance_orders (bus_id, kind, description, opened_by) VALUES (?, ?, ?, ?)`,
		b.ID, req.Kind, desc, current(r).ID)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	if len(sets) > 0 {
		if _, err := h.DB.ExecContext(ctx, `UPDATE maintenance_orders SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
			append(setArgs, id)...); err != nil {
			return err
		}
	}
	if b.Status != "workshop" {
		if err := h.setBusStatus(ctx, b.ID, "workshop"); err != nil {
			return err
		}
	}
	h.audit(r, "abrió orden de mantenimiento %s de la Unidad %s: %s", maintenanceKinds[req.Kind], b.Number, desc)
	o, err := h.loadMaintenance(ctx, uint64(id))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, o)
	return nil
}

// UpdateMaintenance is PATCH /api/v1/admin/maintenance/{id} {"notes"?, "close"?: true, "date"?: "YYYY-MM-DD",
// plus the service-order fields ("km", "partida", "recurso", "execution", "items"), editable while open.
// Closing releases the bus (status active, ramp working) and sets its last maintenance to date
// (default: today in the server's calendar).
func (h *Handler) UpdateMaintenance(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Notes *string `json:"notes"`
		Close bool    `json:"close"`
		Date  string  `json:"date"`
		serviceFields
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	o, err := h.loadMaintenance(ctx, id)
	if err != nil {
		return err
	}
	if o.ClosedAt != nil {
		return httpx.NewError(http.StatusConflict, "closed", "La orden ya está cerrada")
	}
	if req.Notes != nil {
		notes, err := cleanText(*req.Notes, 2000, "Las notas")
		if err != nil {
			return err
		}
		if _, err := h.DB.ExecContext(ctx, `UPDATE maintenance_orders SET notes = ? WHERE id = ?`, notes, id); err != nil {
			return err
		}
	}
	sets, setArgs, err := req.serviceFields.sets()
	if err != nil {
		return err
	}
	if len(sets) > 0 {
		if _, err := h.DB.ExecContext(ctx, `UPDATE maintenance_orders SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
			append(setArgs, id)...); err != nil {
			return err
		}
	}
	if req.Close {
		day := req.Date
		if day == "" {
			day = time.Now().Format(time.DateOnly)
		} else if _, err := time.Parse(time.DateOnly, day); err != nil {
			return httpx.BadRequest("date debe ser YYYY-MM-DD")
		}
		res, err := h.DB.ExecContext(ctx, `UPDATE maintenance_orders SET closed_at = CURRENT_TIMESTAMP(3), closed_by = ?
			WHERE id = ? AND closed_at IS NULL`, current(r).ID, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return httpx.NewError(http.StatusConflict, "closed", "La orden ya está cerrada")
		}
		if _, err := h.DB.ExecContext(ctx, `UPDATE buses SET last_maintenance = ? WHERE id = ?`, day, o.BusID); err != nil {
			return err
		}
		if err := h.setBusStatus(ctx, o.BusID, "active"); err != nil {
			return err
		}
		h.audit(r, "cerró la orden de mantenimiento de la Unidad %s y la liberó", o.BusNumber)
	} else if req.Notes != nil || len(sets) > 0 {
		h.audit(r, "editó la orden de mantenimiento de la Unidad %s", o.BusNumber)
	}
	o, err = h.loadMaintenance(ctx, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, o)
	return nil
}

package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/httpx"
)

// Rider accounts, for the SuperAdmin only: they hold personal and accessibility data. Ramp requests
// and reports stay anonymous, so nothing here links a rider to their trips.

type riderAccount struct {
	ID              uint64    `json:"id"`
	Email           string    `json:"email"`
	Name            string    `json:"name"`
	UserType        string    `json:"userType"`
	Wheelchair      bool      `json:"wheelchair"`
	LowVision       bool      `json:"lowVision"`
	ReducedMobility bool      `json:"reducedMobility"`
	Active          bool      `json:"active"`
	Locked          bool      `json:"locked"`
	CreatedAt       time.Time `json:"createdAt"`
}

const riderColumns = `id, email, name, user_type, wheelchair, low_vision, reduced_mobility, active, locked_until, created_at`

func scanRider(sc interface{ Scan(...any) error }) (riderAccount, error) {
	var x riderAccount
	var locked sql.NullTime
	err := sc.Scan(&x.ID, &x.Email, &x.Name, &x.UserType, &x.Wheelchair, &x.LowVision, &x.ReducedMobility, &x.Active, &locked, &x.CreatedAt)
	x.Locked = auth.Locked(locked)
	return x, err
}

func (h *Handler) loadRider(ctx context.Context, id uint64) (riderAccount, error) {
	x, err := scanRider(h.DB.QueryRowContext(ctx, `SELECT `+riderColumns+` FROM riders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return x, httpx.ErrNotFound
	}
	return x, err
}

// ListRiders is GET /api/v1/admin/riders?q=texto&limit=N: {"total", "wheelchair", "lowVision",
// "reducedMobility", "suspended", "riders": [...]}; q searches name and email.
func (h *Handler) ListRiders(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	out := map[string]any{}
	var total, wheel, vision, mobility, suspended int
	if err := h.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(wheelchair), 0), COALESCE(SUM(low_vision), 0),
		COALESCE(SUM(reduced_mobility), 0), COALESCE(SUM(NOT active), 0) FROM riders`).
		Scan(&total, &wheel, &vision, &mobility, &suspended); err != nil {
		return err
	}
	out["total"], out["wheelchair"], out["lowVision"], out["reducedMobility"], out["suspended"] = total, wheel, vision, mobility, suspended

	q, args := `SELECT `+riderColumns+` FROM riders`, []any{}
	if s := strings.TrimSpace(r.URL.Query().Get("q")); s != "" {
		if len(s) > 100 {
			return httpx.BadRequest("Búsqueda demasiado larga")
		}
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s) + "%"
		q, args = q+` WHERE name LIKE ? OR email LIKE ?`, append(args, like, like)
	}
	rows, err := h.DB.QueryContext(ctx, q+` ORDER BY id DESC LIMIT ?`, append(args, limitParam(r))...)
	if err != nil {
		return err
	}
	defer rows.Close()
	list := []riderAccount{}
	for rows.Next() {
		x, err := scanRider(rows)
		if err != nil {
			return err
		}
		list = append(list, x)
	}
	out["riders"] = list
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// UpdateRider is PATCH /api/v1/admin/riders/{id} {"active"?, "unlock"?: true, "password"?}.
func (h *Handler) UpdateRider(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Active   *bool   `json:"active"`
		Unlock   bool    `json:"unlock"`
		Password *string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	before, err := h.loadRider(ctx, id)
	if err != nil {
		return err
	}
	sets, args, changes := []string{}, []any{}, []string{}
	if req.Active != nil && *req.Active != before.Active {
		sets, args = append(sets, "active = ?"), append(args, *req.Active)
		changes = append(changes, map[bool]string{true: "reactivada", false: "suspendida"}[*req.Active])
	}
	if req.Unlock {
		sets, changes = append(sets, "failed_logins = 0", "locked_until = NULL"), append(changes, "desbloqueada")
	}
	if req.Password != nil {
		if err := validPassword(*req.Password); err != nil {
			return err
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			return err
		}
		sets, args = append(sets, "password_hash = ?", "failed_logins = 0", "locked_until = NULL"), append(args, hash)
		changes = append(changes, "contraseña")
	}
	if len(sets) == 0 {
		httpx.WriteJSON(w, http.StatusOK, before)
		return nil
	}
	if _, err := h.DB.ExecContext(ctx, `UPDATE riders SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		return err
	}
	h.audit(r, "cuenta de pasajero #%d: %s", id, strings.Join(changes, ", "))
	x, err := h.loadRider(ctx, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, x)
	return nil
}

// DeleteRider is DELETE /api/v1/admin/riders/{id}: for a rider who asks for their data to be erased.
func (h *Handler) DeleteRider(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	res, err := h.DB.ExecContext(r.Context(), `DELETE FROM riders WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	// Only the number: the name and email are gone and must not survive in the audit log.
	h.audit(r, "eliminó la cuenta de pasajero #%d", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/riders"
	"github.com/go-sql-driver/mysql"
)

// Role is what a panel account may do. The server enforces it on every route; the panel only
// hides the screens a role can't use.
type Role string

const (
	SuperAdmin Role = "superadmin" // everything, including accounts and routes
	Dispatcher Role = "dispatcher" // live map, drivers and scheduled shifts
	Mechanic   Role = "mechanic"   // fleet status only
)

var (
	AllRoles     = []Role{SuperAdmin, Dispatcher, Mechanic}
	usernameRule = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,40}$`)
)

func validRole(r Role) bool { return slices.Contains(AllRoles, r) }

// Account is the signed-in panel user, loaded on every request so a deactivation or a role change
// takes effect immediately instead of when the 8-hour token expires.
type Account struct {
	ID       uint64 `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     Role   `json:"role"`
}

type accountKey struct{}

func current(r *http.Request) *Account {
	a, _ := r.Context().Value(accountKey{}).(*Account)
	return a
}

// Allow runs after Tokens.Require(RoleAdmin): it rejects deactivated accounts and roles not listed.
func (h *Handler) Allow(roles ...Role) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := strconv.ParseUint(auth.FromContext(r.Context()).Subject, 10, 64)
			if err != nil {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			a := &Account{ID: id}
			var active bool
			err = h.DB.QueryRowContext(r.Context(), `SELECT name, COALESCE(username, ''), email, role, active FROM admins WHERE id = ?`, id).
				Scan(&a.Name, &a.Username, &a.Email, &a.Role, &active)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			if err != nil {
				httpx.WriteError(w, r, err)
				return
			}
			if !slices.Contains(roles, a.Role) {
				httpx.WriteError(w, r, httpx.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountKey{}, a)))
		})
	}
}

// audit records a change made from the panel. A failure is logged, not returned: the change itself
// already happened and the user must not retry it.
func (h *Handler) audit(r *http.Request, format string, args ...any) {
	a := current(r)
	action := fmt.Sprintf(format, args...)
	if utf8.RuneCountInString(action) > 500 {
		action = string([]rune(action)[:500])
	}
	if _, err := h.DB.ExecContext(r.Context(), `INSERT INTO audit_log (admin_id, actor, action) VALUES (?, ?, ?)`,
		a.ID, a.Username, action); err != nil {
		slog.Error("registrando auditoría", "error", err, "accion", action)
	}
}

// Me is GET /api/v1/admin/me: who the panel is signed in as.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, current(r))
	return nil
}

// Audit is GET /api/v1/admin/audit?limit=N: newest last, as the panel's terminal shows it.
func (h *Handler) Audit(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT id, at, actor, action FROM
		(SELECT id, at, actor, action FROM audit_log ORDER BY id DESC LIMIT ?) t ORDER BY id`, limitParam(r))
	if err != nil {
		return err
	}
	defer rows.Close()
	type entry struct {
		ID     uint64    `json:"id"`
		At     time.Time `json:"at"`
		Actor  string    `json:"actor"`
		Action string    `json:"action"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action); err != nil {
			return err
		}
		out = append(out, e)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// ---- panel accounts ----

type user struct {
	ID        uint64    `json:"id"`
	Name      string    `json:"name"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	Active    bool      `json:"active"`
	Locked    bool      `json:"locked"`
	CreatedAt time.Time `json:"createdAt"`
}

const userColumns = `id, name, COALESCE(username, ''), email, role, active, locked_until, created_at`

func scanUser(sc interface{ Scan(...any) error }) (user, error) {
	var u user
	var locked sql.NullTime
	err := sc.Scan(&u.ID, &u.Name, &u.Username, &u.Email, &u.Role, &u.Active, &locked, &u.CreatedAt)
	u.Locked = auth.Locked(locked)
	return u, err
}

func (h *Handler) loadUser(ctx context.Context, id uint64) (user, error) {
	u, err := scanUser(h.DB.QueryRowContext(ctx, `SELECT `+userColumns+` FROM admins WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, httpx.ErrNotFound
	}
	return u, err
}

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT `+userColumns+` FROM admins ORDER BY active DESC, name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []user{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return err
		}
		out = append(out, u)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

func cleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 120 {
		return "", httpx.BadRequest("Nombre inválido")
	}
	return s, nil
}

// CreateUser is POST /api/v1/admin/users {"name", "username", "email", "role", "password"}.
func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name     string `json:"name"`
		Username string `json:"username"`
		Email    string `json:"email"`
		Role     Role   `json:"role"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	name, err := cleanName(req.Name)
	if err != nil {
		return err
	}
	if req.Username = strings.TrimSpace(req.Username); !usernameRule.MatchString(req.Username) {
		return httpx.BadRequest("Usuario inválido (3 a 40 letras, números, _ . -)")
	}
	email, err := riders.NormalizeEmail(req.Email)
	if err != nil {
		return err
	}
	if !validRole(req.Role) {
		return httpx.BadRequest("Rol inválido")
	}
	if err := validPassword(req.Password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	res, err := h.DB.ExecContext(r.Context(), `INSERT INTO admins (name, username, email, role, password_hash) VALUES (?, ?, ?, ?, ?)`,
		name, req.Username, email, req.Role, hash)
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok && me.Number == 1062 {
		return httpx.NewError(http.StatusConflict, "exists", "Ya existe una cuenta con ese usuario o correo")
	}
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	h.audit(r, "creó la cuenta %s (%s)", req.Username, req.Role)
	u, err := h.loadUser(r.Context(), uint64(id))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, u)
	return nil
}

// UpdateUser is PATCH /api/v1/admin/users/{id} {"name"?, "email"?, "role"?, "active"?, "password"?}.
// Nobody can deactivate or demote themselves, and the last active SuperAdmin can't be removed.
func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		Role     *Role   `json:"role"`
		Active   *bool   `json:"active"`
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
	before, err := h.loadUser(ctx, id)
	if err != nil {
		return err
	}
	self := id == current(r).ID

	sets, args, changes := []string{}, []any{}, []string{}
	if req.Name != nil {
		name, err := cleanName(*req.Name)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "name = ?"), append(args, name), append(changes, "nombre")
	}
	if req.Email != nil {
		email, err := riders.NormalizeEmail(*req.Email)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "email = ?"), append(args, email), append(changes, "correo")
	}
	if req.Role != nil && *req.Role != before.Role {
		if !validRole(*req.Role) {
			return httpx.BadRequest("Rol inválido")
		}
		if self {
			return httpx.BadRequest("No puedes cambiar tu propio rol")
		}
		sets, args = append(sets, "role = ?"), append(args, *req.Role)
		changes = append(changes, "rol a "+string(*req.Role))
	}
	if req.Active != nil && *req.Active != before.Active {
		if self {
			return httpx.BadRequest("No puedes dar de baja tu propia cuenta")
		}
		sets, args = append(sets, "active = ?"), append(args, *req.Active)
		changes = append(changes, map[bool]string{true: "reactivada", false: "dada de baja"}[*req.Active])
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

	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE admins SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok && me.Number == 1062 {
		return httpx.NewError(http.StatusConflict, "exists", "Otra cuenta ya usa ese correo")
	}
	if err != nil {
		return err
	}
	var supers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins WHERE role = 'superadmin' AND active FOR UPDATE`).Scan(&supers); err != nil {
		return err
	}
	if supers == 0 {
		return httpx.NewError(http.StatusConflict, "last_superadmin", "Debe quedar al menos un SuperAdmin activo")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	h.audit(r, "editó la cuenta %s: %s", before.Username, strings.Join(changes, ", "))
	u, err := h.loadUser(ctx, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, u)
	return nil
}

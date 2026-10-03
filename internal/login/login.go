// Package login is the single sign-in of the app: one email and password; the server decides
// whether the person is a driver (driver screens) or a rider (rider screens).
package login

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/riders"
)

type Handler struct {
	DB        *sql.DB
	Tokens    *auth.Tokens
	Riders    *riders.Handler
	DriverTTL time.Duration
}

type Driver struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Login is POST /api/v1/auth/login {"email", "password"}.
//
//	driver → {"role":"driver","token","expiresAt","driver":{id,name,email}}  (token: driver_account)
//	rider  → {"role":"rider","token","expiresAt","rider":{…profile}}
//
// Wrong email and wrong password look the same (401, same timing) so accounts can't be enumerated.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || len(email) > 190 || len(req.Password) > 4*riders.MaxPassword {
		auth.BurnTime(req.Password)
		return httpx.ErrUnauthorized
	}
	ctx := r.Context()

	var (
		d           Driver
		hash        string
		active      bool
		lockedUntil sql.NullTime
	)
	err := h.DB.QueryRowContext(ctx, `SELECT id, name, email, password_hash, active, locked_until FROM drivers
		WHERE email = ? AND password_hash IS NOT NULL`, email).
		Scan(&d.ID, &d.Name, &d.Email, &hash, &active, &lockedUntil)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Not a driver: try riders below.
	case err != nil:
		return err
	default:
		return h.driverLogin(w, r, d, hash, active, lockedUntil, req.Password)
	}

	profile, found, err := h.Riders.Authenticate(ctx, email, req.Password)
	if !found {
		auth.BurnTime(req.Password)
		return httpx.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	return h.Riders.WriteSession(w, http.StatusOK, profile)
}

func (h *Handler) driverLogin(w http.ResponseWriter, r *http.Request, d Driver, hash string, active bool,
	lockedUntil sql.NullTime, password string) error {
	ctx := r.Context()
	if !active {
		// Deactivated by an admin: indistinguishable from a wrong password.
		auth.BurnTime(password)
		return httpx.ErrUnauthorized
	}
	if auth.Locked(lockedUntil) {
		return httpx.NewError(http.StatusTooManyRequests, "locked", "Demasiados intentos fallidos, espera 15 minutos")
	}
	ok, err := auth.VerifyPassword(password, hash)
	if err != nil {
		return err
	}
	if !ok {
		if err := auth.DriverAccounts.RecordFailure(ctx, h.DB, d.ID); err != nil {
			return err
		}
		return httpx.ErrUnauthorized
	}
	if err := auth.DriverAccounts.RecordSuccess(ctx, h.DB, d.ID); err != nil {
		return err
	}
	token, exp, err := h.Tokens.Issue(auth.Claims{Role: auth.RoleDriverAccount, RegisteredClaims: auth.Subject(d.ID)}, h.DriverTTL)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"role": "driver", "token": token, "expiresAt": exp, "driver": d})
	return nil
}

// DriverMe is GET /api/v1/driver/me with a driver_account token: re-validates a stored session
// (a deactivated driver is signed out).
func (h *Handler) DriverMe(w http.ResponseWriter, r *http.Request) error {
	var d Driver
	var active bool
	err := h.DB.QueryRowContext(r.Context(), `SELECT id, name, COALESCE(email, ''), active FROM drivers WHERE id = ?`,
		auth.FromContext(r.Context()).Subject).Scan(&d.ID, &d.Name, &d.Email, &active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
		return httpx.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

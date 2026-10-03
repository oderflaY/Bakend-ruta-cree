// Package riders handles the optional rider accounts: sign-up, login, and the accessibility
// profile (assistance type and favorite stops) that the app syncs across devices.
// Ramp requests and reports stay anonymous and are never linked to an account.
package riders

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/go-sql-driver/mysql"
)

const (
	MinPassword = 8
	MaxPassword = 128
	maxName     = 120
	maxFavorite = 120
)

type Handler struct {
	DB       *sql.DB
	Tokens   *auth.Tokens
	TokenTTL time.Duration
}

type Favorites struct {
	Home    string `json:"home"`
	Center  string `json:"center"`
	Therapy string `json:"therapy"`
}

type Profile struct {
	ID              uint64    `json:"id"`
	Email           string    `json:"email"`
	Name            string    `json:"name"`
	UserType        string    `json:"userType"`
	Wheelchair      bool      `json:"wheelchair"`
	LowVision       bool      `json:"lowVision"`
	ReducedMobility bool      `json:"reducedMobility"`
	Favorites       Favorites `json:"favorites"`
}

type session struct {
	Role      string    `json:"role"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Rider     Profile   `json:"rider"`
}

// Register is POST /api/v1/riders {"email", "password", "name"}.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	email, err := NormalizeEmail(req.Email)
	if err != nil {
		return err
	}
	// One email, one person: a driver's login email can't also be a rider account.
	var taken int
	if err := h.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM drivers WHERE email = ?`, email).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return httpx.NewError(http.StatusConflict, "email_taken", "Ya existe una cuenta con ese correo")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return httpx.BadRequest("Escribe tu nombre (máximo 120 caracteres)")
	}
	if n := utf8.RuneCountInString(req.Password); n < MinPassword || n > MaxPassword {
		return httpx.NewError(http.StatusBadRequest, "weak_password", "La contraseña debe tener entre 8 y 128 caracteres")
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	res, err := h.DB.ExecContext(r.Context(), `INSERT INTO riders (email, name, password_hash) VALUES (?, ?, ?)`, email, name, hash)
	var dup *mysql.MySQLError
	if errors.As(err, &dup) && dup.Number == 1062 {
		return httpx.NewError(http.StatusConflict, "email_taken", "Ya existe una cuenta con ese correo")
	}
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	profile := Profile{ID: uint64(id), Email: email, Name: name, UserType: "rider"}
	return h.WriteSession(w, http.StatusCreated, profile)
}

// Authenticate checks a rider's email and password with lockout. found=false means no rider has
// that email (the caller burns time so the response doesn't reveal it).
func (h *Handler) Authenticate(ctx context.Context, email, password string) (profile Profile, found bool, err error) {
	var (
		id          uint64
		hash        string
		lockedUntil sql.NullTime
		active      bool
	)
	err = h.DB.QueryRowContext(ctx, `SELECT id, password_hash, locked_until, active FROM riders WHERE email = ?`, email).
		Scan(&id, &hash, &lockedUntil, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, true, err
	}
	if auth.Locked(lockedUntil) {
		return Profile{}, true, httpx.NewError(http.StatusTooManyRequests, "locked", "Demasiados intentos fallidos, espera 15 minutos")
	}
	ok, err := auth.VerifyPassword(password, hash)
	if err != nil {
		return Profile{}, true, err
	}
	if !ok {
		if err := auth.RiderAccounts.RecordFailure(ctx, h.DB, id); err != nil {
			return Profile{}, true, err
		}
		return Profile{}, true, httpx.ErrUnauthorized
	}
	// After the password, so a guess can't tell a suspended account from a missing one.
	if !active {
		return Profile{}, true, httpx.NewError(http.StatusForbidden, "suspended", "Tu cuenta está suspendida; comunícate con el CREE")
	}
	if err := auth.RiderAccounts.RecordSuccess(ctx, h.DB, id); err != nil {
		return Profile{}, true, err
	}
	profile, err = h.Load(ctx, id)
	return profile, true, err
}

// Me is GET /api/v1/riders/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) error {
	id, err := riderID(r)
	if err != nil {
		return err
	}
	profile, err := h.Load(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, profile)
	return nil
}

// UpdateProfile is PUT /api/v1/riders/me with the editable fields of Profile.
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) error {
	id, err := riderID(r)
	if err != nil {
		return err
	}
	var req struct {
		Name            string    `json:"name"`
		UserType        string    `json:"userType"`
		Wheelchair      bool      `json:"wheelchair"`
		LowVision       bool      `json:"lowVision"`
		ReducedMobility bool      `json:"reducedMobility"`
		Favorites       Favorites `json:"favorites"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return httpx.BadRequest("Escribe tu nombre (máximo 120 caracteres)")
	}
	if req.UserType != "rider" && req.UserType != "companion" {
		return httpx.BadRequest("userType debe ser rider o companion")
	}
	fav := Favorites{
		Home:    strings.TrimSpace(req.Favorites.Home),
		Center:  strings.TrimSpace(req.Favorites.Center),
		Therapy: strings.TrimSpace(req.Favorites.Therapy),
	}
	for _, f := range []string{fav.Home, fav.Center, fav.Therapy} {
		if utf8.RuneCountInString(f) > maxFavorite {
			return httpx.BadRequest("Cada parada favorita admite máximo 120 caracteres")
		}
	}
	_, err = h.DB.ExecContext(r.Context(), `UPDATE riders SET name = ?, user_type = ?, wheelchair = ?, low_vision = ?,
		reduced_mobility = ?, favorite_home = ?, favorite_center = ?, favorite_therapy = ? WHERE id = ? AND active`,
		name, req.UserType, req.Wheelchair, req.LowVision, req.ReducedMobility, fav.Home, fav.Center, fav.Therapy, id)
	if err != nil {
		return err
	}
	// load also answers 401 if the account was deleted meanwhile.
	profile, err := h.Load(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, profile)
	return nil
}

// Delete is DELETE /api/v1/riders/me: removes the account and its profile for good.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) error {
	id, err := riderID(r)
	if err != nil {
		return err
	}
	if _, err := h.DB.ExecContext(r.Context(), `DELETE FROM riders WHERE id = ?`, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) Load(ctx context.Context, id uint64) (Profile, error) {
	p := Profile{ID: id}
	err := h.DB.QueryRowContext(ctx, `SELECT email, name, user_type, wheelchair, low_vision, reduced_mobility,
		favorite_home, favorite_center, favorite_therapy FROM riders WHERE id = ? AND active`, id).
		Scan(&p.Email, &p.Name, &p.UserType, &p.Wheelchair, &p.LowVision, &p.ReducedMobility,
			&p.Favorites.Home, &p.Favorites.Center, &p.Favorites.Therapy)
	if errors.Is(err, sql.ErrNoRows) {
		// A token for a deleted or suspended account: the app must sign out.
		return p, httpx.ErrUnauthorized
	}
	return p, err
}

func (h *Handler) WriteSession(w http.ResponseWriter, status int, p Profile) error {
	token, exp, err := h.Tokens.Issue(auth.Claims{
		Role: auth.RoleRider, RegisteredClaims: auth.Subject(strconv.FormatUint(p.ID, 10)),
	}, h.TokenTTL)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, status, session{Role: "rider", Token: token, ExpiresAt: exp, Rider: p})
	return nil
}

func riderID(r *http.Request) (uint64, error) {
	c := auth.FromContext(r.Context())
	id, err := strconv.ParseUint(c.Subject, 10, 64)
	if err != nil {
		return 0, httpx.ErrUnauthorized
	}
	return id, nil
}

// NormalizeEmail lower-cases and validates an address (shared by riders and drivers).
func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || len(email) > 190 || !strings.Contains(email[strings.LastIndex(email, "@"):], ".") {
		return "", httpx.NewError(http.StatusBadRequest, "invalid_email", "Correo electrónico inválido")
	}
	return email, nil
}

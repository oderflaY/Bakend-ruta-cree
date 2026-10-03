package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/golang-jwt/jwt/v5"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleDriver Role = "driver"
	RoleRider  Role = "rider"
	// A signed-in driver without an open shift: may only start a shift and read their account.
	RoleDriverAccount Role = "driver_account"
)

const issuer = "rutacree-api"

type Claims struct {
	Role Role `json:"role"`
	// Driver tokens are bound to one shift: when the shift ends the token stops working.
	ShiftID uint64 `json:"sid,omitempty"`
	BusID   uint64 `json:"bus,omitempty"`
	RouteID string `json:"rte,omitempty"`
	jwt.RegisteredClaims
}

type Tokens struct {
	secret []byte
	parser *jwt.Parser
}

func NewTokens(secret []byte) *Tokens {
	return &Tokens{
		secret: secret,
		// Pin the algorithm: never accept "none" or an RS/HS confusion.
		parser: jwt.NewParser(
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithLeeway(30*time.Second),
		),
	}
}

// Subject builds the registered claims for a token issued to `id`.
func Subject(id string) jwt.RegisteredClaims { return jwt.RegisteredClaims{Subject: id} }

func (t *Tokens) Issue(c Claims, ttl time.Duration) (string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", time.Time{}, err
	}
	c.Issuer = issuer
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(exp)
	c.ID = hex.EncodeToString(jti)
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.secret)
	return s, exp, err
}

func (t *Tokens) Parse(raw string) (*Claims, error) {
	c := &Claims{}
	if _, err := t.parser.ParseWithClaims(raw, c, func(*jwt.Token) (any, error) { return t.secret, nil }); err != nil {
		return nil, err
	}
	return c, nil
}

// ---- middleware ----

type ctxKey struct{}

func FromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(ctxKey{}).(*Claims)
	return c
}

// ShiftChecker reports whether a driver shift is still open (kept in memory by the live hub).
type ShiftChecker interface {
	ShiftActive(shiftID uint64) bool
}

// Require accepts only a Bearer token with the given role. Tokens in the query string are refused
// so they don't end up in proxy logs.
func (t *Tokens) Require(role Role, shifts ShiftChecker) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || raw == "" {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			c, err := t.Parse(strings.TrimSpace(raw))
			if err != nil || c.Subject == "" {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			if c.Role != role {
				httpx.WriteError(w, r, httpx.ErrForbidden)
				return
			}
			if role == RoleDriver && (c.ShiftID == 0 || shifts == nil || !shifts.ShiftActive(c.ShiftID)) {
				httpx.WriteError(w, r, httpx.NewError(http.StatusUnauthorized, "shift_closed", "El turno ya terminó"))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
		})
	}
}

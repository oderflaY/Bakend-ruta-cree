// Package httpx holds the HTTP plumbing shared by every handler: JSON I/O, errors and middleware.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

const maxBodyBytes = 64 << 10 // 64 KiB: the largest legit body is a route GeoJSON upload (admin sets its own limit).

// Error is returned to clients as {"error": code, "message": text}. Internal errors never leak details.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NewError(status int, code, msg string) *Error { return &Error{status, code, msg} }

var (
	ErrUnauthorized = NewError(http.StatusUnauthorized, "unauthorized", "Credenciales inválidas o sesión expirada")
	ErrForbidden    = NewError(http.StatusForbidden, "forbidden", "No tienes permiso para esta acción")
	ErrNotFound     = NewError(http.StatusNotFound, "not_found", "No encontrado")
	ErrTooMany      = NewError(http.StatusTooManyRequests, "rate_limited", "Demasiadas solicitudes, intenta más tarde")
)

func BadRequest(msg string) *Error { return NewError(http.StatusBadRequest, "bad_request", msg) }

// HandlerFunc lets handlers return errors instead of writing them by hand.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

func Handle(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	})
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("error interno", "method", r.Method, "path", r.URL.Path, "error", err)
		e = NewError(http.StatusInternalServerError, "internal", "Error interno del servidor")
	}
	WriteJSON(w, e.Status, map[string]string{"error": e.Code, "message": e.Message})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// DecodeJSON reads a size-limited body and rejects unknown fields and trailing data.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	return DecodeJSONLimit(w, r, dst, maxBodyBytes)
}

func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return NewError(http.StatusUnsupportedMediaType, "unsupported_media_type", "Se espera Content-Type: application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return NewError(http.StatusRequestEntityTooLarge, "too_large", "Cuerpo demasiado grande")
		}
		return BadRequest(fmt.Sprintf("JSON inválido: %v", err))
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return BadRequest("JSON inválido: datos extra al final")
	}
	return nil
}

// ---- middleware ----

type Middleware func(http.Handler) http.Handler

func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "value", v, "stack", string(debug.Stack()))
				WriteError(w, r, fmt.Errorf("panic: %v", v))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders is strict because the API serves JSON only; no HTML is ever rendered.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		h.Set("Cache-Control", "no-store")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// CORS only for the configured dashboard origins; the mobile app doesn't need it.
func CORS(allowed []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && slices.Contains(allowed, origin) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Max-Age", "600")
			}
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int)        { s.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Logger logs method, path and status. Query strings are left out: they may carry tokens.
func Logger(ips *ClientIP) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			slog.Log(r.Context(), level, "http", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "ms", time.Since(start).Milliseconds(), "ip", ips.Of(r))
		})
	}
}

// ClientIP resolves the caller's address, honoring X-Forwarded-For only from trusted proxies
// (otherwise anyone could spoof it to dodge the rate limits).
type ClientIP struct{ trusted []*net.IPNet }

func NewClientIP(trusted []*net.IPNet) *ClientIP { return &ClientIP{trusted} }

func (c *ClientIP) Of(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !c.isTrusted(net.ParseIP(host)) {
		return host
	}
	// Walk right to left, skipping our own proxies; the first untrusted hop is the client.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			break
		}
		if !c.isTrusted(ip) {
			return ip.String()
		}
	}
	return host
}

func (c *ClientIP) isTrusted(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range c.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Deadlines bounds how long a plain request may take to send its body and receive the response,
// so slow clients can't pin connections. WebSocket routes are skipped: they're long-lived by design.
func Deadlines(read, write time.Duration, skip ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !slices.Contains(skip, r.URL.Path) {
				rc := http.NewResponseController(w)
				now := time.Now()
				_ = rc.SetReadDeadline(now.Add(read))
				_ = rc.SetWriteDeadline(now.Add(write))
			}
			next.ServeHTTP(w, r)
		})
	}
}

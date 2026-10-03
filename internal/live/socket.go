package live

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/httpx"
)

const (
	sendBuffer   = 32
	writeTimeout = 10 * time.Second
	pingInterval = 30 * time.Second
)

// client owns one WebSocket. Messages are queued without blocking; a client that can't keep up
// is disconnected instead of slowing down the broadcast for everybody else.
type client struct {
	conn  *websocket.Conn
	queue chan []byte
	once  sync.Once
	done  chan struct{}
	why   string
}

func newClient(conn *websocket.Conn) *client {
	return &client{conn: conn, queue: make(chan []byte, sendBuffer), done: make(chan struct{})}
}

func (c *client) send(b []byte) bool {
	select {
	case c.queue <- b:
		return true
	case <-c.done:
		return false
	default:
		c.close("cliente lento")
		return false
	}
}

func (c *client) close(reason string) {
	c.once.Do(func() {
		c.why = reason
		close(c.done)
	})
}

// run writes queued messages and pings until the client or the server goes away.
func (c *client) run(ctx context.Context) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			c.conn.Close(websocket.StatusGoingAway, "")
			return
		case <-c.done:
			c.conn.Close(websocket.StatusPolicyViolation, c.why)
			return
		case b := <-c.queue:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (h *Hub) closeAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.public {
		c.close("servidor reiniciando")
	}
	for c := range h.drivers {
		c.close("servidor reiniciando")
	}
}

type SocketHandler struct {
	Hub            *Hub
	OriginPatterns []string
	Tokens         *auth.Tokens
	IPs            *httpx.ClientIP
	PerIP          int
}

var ipConns = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

// acquire caps concurrent sockets globally and per IP (so one host can't exhaust the server).
func (s *SocketHandler) acquire(r *http.Request) (release func(), ok bool) {
	ip := s.IPs.Of(r)
	s.Hub.mu.RLock()
	total := len(s.Hub.public) + len(s.Hub.drivers)
	s.Hub.mu.RUnlock()
	ipConns.Lock()
	defer ipConns.Unlock()
	if total >= s.Hub.maxClients || ipConns.n[ip] >= s.PerIP {
		return nil, false
	}
	ipConns.n[ip]++
	return func() {
		ipConns.Lock()
		if ipConns.n[ip]--; ipConns.n[ip] <= 0 {
			delete(ipConns.n, ip)
		}
		ipConns.Unlock()
	}, true
}

func (s *SocketHandler) accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Browsers must come from an allowed origin; native apps send no Origin and pass.
		OriginPatterns: s.OriginPatterns,
		// Messages are small; per-connection compressors would cost ~1 MB each at thousands of riders.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4 << 10) // clients only send control frames
	return conn, nil
}

// ServePublic is GET /api/socket: the live map for riders. No login: bus positions are public
// information, and nothing about riders is sent or stored.
func (s *SocketHandler) ServePublic(w http.ResponseWriter, r *http.Request) {
	release, ok := s.acquire(r)
	if !ok {
		httpx.WriteError(w, r, httpx.ErrTooMany)
		return
	}
	defer release()
	conn, err := s.accept(w, r)
	if err != nil {
		return
	}
	c := newClient(conn)
	c.queue <- s.Hub.snapshot()

	s.Hub.mu.Lock()
	s.Hub.public[c] = struct{}{}
	s.Hub.mu.Unlock()
	defer func() {
		s.Hub.mu.Lock()
		delete(s.Hub.public, c)
		s.Hub.mu.Unlock()
	}()

	c.run(conn.CloseRead(r.Context()))
}

// ServeDriver is GET /api/v1/driver/socket (Bearer token of an open shift): ramp requests for the
// driver's route. Pending requests are replayed on connect so none is lost across reconnects.
func (s *SocketHandler) ServeDriver(pending func(ctx context.Context, routeID string) []RampRequest) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := auth.FromContext(r.Context())
		shift, ok := s.Hub.Shift(claims.ShiftID)
		if !ok {
			httpx.WriteError(w, r, httpx.ErrUnauthorized)
			return
		}
		release, ok := s.acquire(r)
		if !ok {
			httpx.WriteError(w, r, httpx.ErrTooMany)
			return
		}
		defer release()
		conn, err := s.accept(w, r)
		if err != nil {
			return
		}
		c := newClient(conn)
		if reqs := pending(r.Context(), shift.RouteID); len(reqs) > 0 {
			if b, err := jsonMessage(message{RampRequests: reqs}); err == nil {
				c.queue <- b
			}
		}

		s.Hub.mu.Lock()
		if _, still := s.Hub.shifts[shift.ID]; !still {
			s.Hub.mu.Unlock()
			conn.Close(websocket.StatusPolicyViolation, "turno terminado")
			return
		}
		s.Hub.drivers[c] = shift.ID
		s.Hub.mu.Unlock()
		defer func() {
			s.Hub.mu.Lock()
			delete(s.Hub.drivers, c)
			s.Hub.mu.Unlock()
		}()

		slog.Info("chofer conectado", "turno", shift.ID, "ruta", shift.RouteID)
		c.run(conn.CloseRead(r.Context()))
	}
}

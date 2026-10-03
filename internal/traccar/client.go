// Package traccar connects the backend to a Traccar server: the driver's phone runs Traccar Client
// (OsmAnd protocol), Traccar stores the fixes, and this package streams them into the live hub.
package traccar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// Client talks to Traccar's REST API and WebSocket with a service account (session cookie).
type Client struct {
	base     *url.URL
	email    string
	password string
	http     *http.Client
}

func NewClient(baseURL, email, password string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("TRACCAR_URL inválida: %q", baseURL)
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: u, email: email, password: password,
		http: &http.Client{Jar: jar, Timeout: 15 * time.Second},
	}, nil
}

type Device struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	UniqueID string `json:"uniqueId"`
	Status   string `json:"status"`
}

type Position struct {
	DeviceID  int64     `json:"deviceId"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Speed     float64   `json:"speed"` // knots
	Valid     bool      `json:"valid"`
	FixTime   time.Time `json:"fixTime"`
}

type socketMessage struct {
	Devices   []Device   `json:"devices"`
	Positions []Position `json:"positions"`
}

var errUnauthorized = errors.New("traccar: credenciales rechazadas")

// Login opens a session. On a brand-new Traccar with no users it registers the service account
// first; Traccar makes the first registered user an administrator.
func (c *Client) Login(ctx context.Context) error {
	err := c.session(ctx)
	if !errors.Is(err, errUnauthorized) {
		return err
	}
	if regErr := c.registerFirstUser(ctx); regErr != nil {
		return fmt.Errorf("%w (y no se pudo registrar la cuenta de servicio: %v)", err, regErr)
	}
	return c.session(ctx)
}

func (c *Client) session(ctx context.Context) error {
	form := url.Values{"email": {c.email}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url("/api/session"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer drain(res)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		return errUnauthorized
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("traccar /api/session: %s", res.Status)
	}
	return nil
}

func (c *Client) registerFirstUser(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"name": "RutaCree API", "email": c.email, "password": c.password})
	res, err := c.do(ctx, http.MethodPost, "/api/users", body)
	if err != nil {
		return err
	}
	defer drain(res)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("traccar /api/users: %s", res.Status)
	}
	return nil
}

// DisableRegistration turns off self sign-up on Traccar's web UI: only the service account (an
// admin) and the users it creates may log in.
func (c *Client) DisableRegistration(ctx context.Context) error {
	res, err := c.do(ctx, http.MethodGet, "/api/server", nil)
	if err != nil {
		return err
	}
	var server map[string]any
	if err := decode(res, &server); err != nil {
		return err
	}
	if open, _ := server["registration"].(bool); !open {
		return nil
	}
	server["registration"] = false
	body, _ := json.Marshal(server)
	res, err = c.do(ctx, http.MethodPut, "/api/server", body)
	if err != nil {
		return err
	}
	defer drain(res)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("traccar PUT /api/server: %s", res.Status)
	}
	return nil
}

// EnsureDevice returns the Traccar device whose identifier is uniqueID, creating it if needed.
// The identifier is the bus number, which is what the driver types in Traccar Client.
func (c *Client) EnsureDevice(ctx context.Context, uniqueID, name string) (Device, error) {
	res, err := c.do(ctx, http.MethodGet, "/api/devices?uniqueId="+url.QueryEscape(uniqueID), nil)
	if err != nil {
		return Device{}, err
	}
	var found []Device
	err = decode(res, &found)
	if err != nil {
		return Device{}, err
	}
	if len(found) > 0 {
		return found[0], nil
	}
	body, _ := json.Marshal(Device{Name: name, UniqueID: uniqueID})
	res, err = c.do(ctx, http.MethodPost, "/api/devices", body)
	if err != nil {
		return Device{}, err
	}
	var created Device
	return created, decode(res, &created)
}

func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	res, err := c.do(ctx, http.MethodGet, "/api/devices", nil)
	if err != nil {
		return nil, err
	}
	var out []Device
	return out, decode(res, &out)
}

// Stream delivers every device and position update until ctx ends or the connection drops.
func (c *Client) Stream(ctx context.Context, onDevices func([]Device), onPositions func([]Position)) error {
	wsURL := *c.base
	wsURL.Scheme = map[string]string{"https": "wss", "http": "ws"}[c.base.Scheme]
	wsURL.Path = strings.TrimRight(c.base.Path, "/") + "/api/socket"
	header := http.Header{}
	for _, ck := range c.http.Jar.Cookies(c.base) {
		header.Add("Cookie", ck.Name+"="+ck.Value)
	}
	conn, res, err := websocket.Dial(ctx, wsURL.String(), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if res != nil && res.StatusCode == http.StatusUnauthorized {
			return errUnauthorized
		}
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var m socketMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if len(m.Devices) > 0 {
			onDevices(m.Devices)
		}
		if len(m.Positions) > 0 {
			onPositions(m.Positions)
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	send := func() (*http.Response, error) {
		var r io.Reader
		if body != nil {
			r = strings.NewReader(string(body))
		}
		req, err := http.NewRequestWithContext(ctx, method, c.url(path), r)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return c.http.Do(req)
	}
	res, err := send()
	if err != nil {
		return nil, err
	}
	// The session expired (Traccar restarted): log in again once and retry.
	if res.StatusCode == http.StatusUnauthorized && path != "/api/users" {
		drain(res)
		if err := c.session(ctx); err != nil {
			return nil, err
		}
		return send()
	}
	return res, nil
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.base.String(), "/") + path
}

func decode(res *http.Response, dst any) error {
	defer drain(res)
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("traccar %s: %s", res.Request.URL.Path, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(dst)
}

func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
}

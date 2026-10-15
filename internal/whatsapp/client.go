// Package whatsapp is the drivers' chatbot on the WhatsApp Cloud API: drivers start and end their
// route, give the passenger count, report failures and receive ramp requests by chatting with the
// dispatch number. The driver is identified by the phone registered in the panel.
package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Button is a quick-reply button (at most 3 per message, title up to 20 characters).
type Button struct{ ID, Title string }

// Row is an option of a list message (at most 10, title up to 24 characters).
type Row struct{ ID, Title, Description string }

// Sender sends messages to a driver; *Client in production, a fake in tests.
type Sender interface {
	Text(ctx context.Context, to, body string) error
	Buttons(ctx context.Context, to, body string, buttons []Button) error
	List(ctx context.Context, to, body, button string, rows []Row) error
}

// Client talks to the Graph API: POST {base}/{phoneNumberID}/messages.
type Client struct {
	HTTP    *http.Client
	Base    string // e.g. https://graph.facebook.com/v23.0
	PhoneID string
	Token   string
}

func NewClient(base, phoneID, token string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 15 * time.Second}, Base: strings.TrimRight(base, "/"), PhoneID: phoneID, Token: token}
}

func (c *Client) Text(ctx context.Context, to, body string) error {
	return c.send(ctx, to, "text", map[string]any{"text": map[string]any{"body": body, "preview_url": false}})
}

func (c *Client) Buttons(ctx context.Context, to, body string, buttons []Button) error {
	bs := make([]map[string]any, 0, len(buttons))
	for _, b := range buttons {
		bs = append(bs, map[string]any{"type": "reply", "reply": map[string]any{"id": b.ID, "title": clip(b.Title, 20)}})
	}
	return c.send(ctx, to, "interactive", map[string]any{"interactive": map[string]any{
		"type": "button", "body": map[string]any{"text": body}, "action": map[string]any{"buttons": bs},
	}})
}

func (c *Client) List(ctx context.Context, to, body, button string, rows []Row) error {
	rs := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		row := map[string]any{"id": r.ID, "title": clip(r.Title, 24)}
		if r.Description != "" {
			row["description"] = clip(r.Description, 72)
		}
		rs = append(rs, row)
	}
	return c.send(ctx, to, "interactive", map[string]any{"interactive": map[string]any{
		"type": "list", "body": map[string]any{"text": body},
		"action": map[string]any{"button": clip(button, 20), "sections": []any{map[string]any{"title": "Opciones", "rows": rs}}},
	}})
}

func (c *Client) send(ctx context.Context, to, kind string, content map[string]any) error {
	msg := map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": to, "type": kind}
	for k, v := range content {
		msg[k] = v
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/"+c.PhoneID+"/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("whatsapp %d: %s", resp.StatusCode, detail)
	}
	return nil
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

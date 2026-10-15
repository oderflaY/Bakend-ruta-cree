package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Webhook is GET/POST /api/whatsapp/webhook, the URL set in the Meta app.
type Webhook struct {
	Bot *Bot
	// Chosen when configuring the webhook in Meta; Meta echoes it to prove the URL is ours.
	VerifyToken string
	// The Meta app's secret: every POST is signed with it (X-Hub-Signature-256).
	AppSecret string
	// Messages are answered in the background, outliving the request.
	Ctx context.Context

	mu   sync.Mutex
	seen map[string]time.Time // Meta may deliver a message more than once
}

// Verify answers Meta's subscription check.
func (h *Webhook) Verify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") != "subscribe" ||
		subtle.ConstantTimeCompare([]byte(q.Get("hub.verify_token")), []byte(h.VerifyToken)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = io.WriteString(w, q.Get("hub.challenge"))
}

type payload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From        string                `json:"from"`
					ID          string                `json:"id"`
					Type        string                `json:"type"`
					Text        struct{ Body string } `json:"text"`
					Interactive struct {
						ButtonReply struct{ ID string } `json:"button_reply"`
						ListReply   struct{ ID string } `json:"list_reply"`
					} `json:"interactive"`
					Button struct{ Payload, Text string } `json:"button"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// Receive takes Meta's notifications. It answers 200 at once (Meta retries slow or failed
// deliveries) and handles each driver message in the background.
func (h *Webhook) Receive(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validSignature(h.AppSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		slog.Warn("webhook de WhatsApp con firma inválida")
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)

	for _, e := range p.Entry {
		for _, c := range e.Changes {
			for _, m := range c.Value.Messages {
				if m.From == "" || h.duplicate(m.ID) {
					continue
				}
				in := Incoming{ID: m.ID, From: m.From}
				switch m.Type {
				case "text":
					in.Text = m.Text.Body
				case "interactive":
					in.Reply = m.Interactive.ButtonReply.ID
					if in.Reply == "" {
						in.Reply = m.Interactive.ListReply.ID
					}
				case "button":
					in.Reply, in.Text = m.Button.Payload, m.Button.Text
				}
				// Audio, photos, stickers…: answered with the menu, like any unknown text.
				go func() {
					ctx, cancel := context.WithTimeout(h.Ctx, 45*time.Second)
					defer cancel()
					h.Bot.Handle(ctx, in)
				}()
			}
		}
	}
}

func (h *Webhook) duplicate(id string) bool {
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	if h.seen == nil {
		h.seen = map[string]time.Time{}
	}
	if len(h.seen) > 5000 {
		for k, t := range h.seen {
			if now.Sub(t) > time.Hour {
				delete(h.seen, k)
			}
		}
	}
	if _, ok := h.seen[id]; ok {
		return true
	}
	h.seen[id] = now
	return false
}

func validSignature(secret string, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok || secret == "" {
		return false
	}
	sig, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

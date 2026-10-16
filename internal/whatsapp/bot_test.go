package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eter/rutacree-backend/internal/dispatch"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/live"
)

const driverPhone = "5216183723492" // WhatsApp adds the old "1" after 52; the panel has 618 372 3492

type sent struct {
	to, body string
	buttons  []Button
	rows     []Row
}

type fakeSender struct {
	mu  sync.Mutex
	out []sent
}

func (f *fakeSender) add(m sent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.out = append(f.out, m)
	return nil
}
func (f *fakeSender) Text(_ context.Context, to, body string) error { return f.add(sent{to: to, body: body}) }
func (f *fakeSender) Buttons(_ context.Context, to, body string, b []Button) error {
	return f.add(sent{to: to, body: body, buttons: b})
}
func (f *fakeSender) List(_ context.Context, to, body, _ string, r []Row) error {
	return f.add(sent{to: to, body: body, rows: r})
}
func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.out)
}
func (f *fakeSender) last() sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.out[len(f.out)-1]
}

type fakeStore struct {
	sessions map[string]Session
	open     *OpenShift
	active   bool
}

func (f *fakeStore) DriverByPhone(_ context.Context, last10 string) (Driver, int, error) {
	if last10 == "6183723492" {
		return Driver{ID: "CH-001", Name: "Juan Pérez", Active: f.active}, 1, nil
	}
	return Driver{}, 0, nil
}
func (f *fakeStore) Session(_ context.Context, phone string) (Session, error) {
	s, ok := f.sessions[phone]
	if !ok {
		s = Session{Phone: phone}
	}
	return s, nil
}
func (f *fakeStore) SaveSession(_ context.Context, s Session) error {
	f.sessions[s.Phone] = s
	return nil
}
func (f *fakeStore) BusStatus(_ context.Context, n string) (string, error) {
	return map[string]string{"22": "active", "LT076": "workshop"}[n], nil
}
func (f *fakeStore) OpenShiftOf(context.Context, string) (OpenShift, bool, error) {
	if f.open == nil {
		return OpenShift{}, false, nil
	}
	return *f.open, true, nil
}
func (f *fakeStore) PhonesOnRoute(context.Context, string) ([]string, error) {
	if f.open == nil {
		return nil, nil
	}
	return []string{driverPhone}, nil
}

type fakeDispatch struct {
	store    *fakeStore
	begun    dispatch.StartRequest
	reported []string
	finished string
	acked    uint64
}

func (f *fakeDispatch) Begin(_ context.Context, req dispatch.StartRequest) (dispatch.Started, error) {
	f.begun = req
	start := time.Date(2026, 10, 6, 13, 30, 0, 0, time.UTC)
	f.store.open = &OpenShift{ID: 7, BusNumber: req.BusNumber, RouteID: req.RouteID, StartedAt: start}
	return dispatch.Started{Shift: live.Shift{ID: 7, BusNumber: req.BusNumber, RouteID: req.RouteID}, StartedAt: start, DriverName: "Juan Pérez"}, nil
}
func (f *fakeDispatch) Report(_ context.Context, shift uint64, kind string) (uint64, error) {
	f.reported = append(f.reported, kind)
	if kind != "ramp_failure" {
		f.store.open = nil
	}
	return 1, nil
}
func (f *fakeDispatch) Finish(_ context.Context, shift uint64, loc string) (time.Time, time.Time, error) {
	f.finished = loc
	f.store.open = nil
	return time.Date(2026, 10, 6, 13, 30, 0, 0, time.UTC), time.Date(2026, 10, 6, 15, 5, 0, 0, time.UTC), nil
}
func (f *fakeDispatch) Acknowledge(_ context.Context, shift, id uint64) error {
	if id == 99 {
		return httpx.NewError(http.StatusConflict, "not_pending", "ya no")
	}
	f.acked = id
	return nil
}

func newBot() (*Bot, *fakeStore, *fakeDispatch, *fakeSender) {
	store := &fakeStore{sessions: map[string]Session{}, active: true}
	disp := &fakeDispatch{store: store}
	send := &fakeSender{}
	mx, _ := time.LoadLocation("America/Mexico_City")
	bot := &Bot{Store: store, Dispatch: disp, Send: send, Location: mx, Routes: func() []RouteInfo {
		return []RouteInfo{{"ruta-1", "Ruta 1 - Oriente Poniente"}, {"ruta-10", "Ruta 10 - Periférico"}, {"ruta-2", "Ruta 2 - Guadalupe Victoria"}}
	}}
	return bot, store, disp, send
}

func say(b *Bot, text string)  { b.Handle(context.Background(), Incoming{From: driverPhone, Text: text}) }
func tap(b *Bot, reply string) { b.Handle(context.Background(), Incoming{From: driverPhone, Reply: reply}) }

func TestUnknownNumberIsToldToRegister(t *testing.T) {
	bot, _, _, send := newBot()
	bot.Handle(context.Background(), Incoming{From: "5215550001111", Text: "hola"})
	if !strings.Contains(send.last().body, "no está registrado") {
		t.Fatalf("got %q", send.last().body)
	}
}

func TestFullRouteByChat(t *testing.T) {
	bot, store, disp, send := newBot()

	say(bot, "hola")
	if b := send.last(); len(b.buttons) != 1 || b.buttons[0].ID != "start" || !strings.Contains(b.body, "Juan") {
		t.Fatalf("idle menu: %+v", b)
	}
	tap(bot, "start")
	say(bot, "99") // not in the fleet
	if !strings.Contains(send.last().body, "No encuentro la unidad") {
		t.Fatalf("unknown bus: %q", send.last().body)
	}
	say(bot, "LT076")
	if !strings.Contains(send.last().body, "taller") {
		t.Fatalf("workshop bus: %q", send.last().body)
	}
	say(bot, "unidad 22")
	if !strings.Contains(send.last().body, "Ruta 10 - Periférico") {
		t.Fatalf("route list: %q", send.last().body)
	}
	say(bot, "Ruta 10") // must not be taken as ruta-1
	say(bot, "cuatro")
	if !strings.Contains(send.last().body, "solo un número") {
		t.Fatalf("bad count: %q", send.last().body)
	}
	for _, n := range []string{"4", "5", "3", "2", "1", "3"} {
		say(bot, n)
	}
	if b := send.last(); len(b.buttons) != 3 || !strings.Contains(b.body, "Hombres: 4 · Mujeres: 5 (total 9)\nDiscapacidad: 3 · Adultos mayores: 2 · Embarazadas: 1 · Acompañantes: 3") {
		t.Fatalf("confirm: %+v", b)
	}
	tap(bot, "ok")
	if disp.begun.BusNumber != "22" || disp.begun.RouteID != "ruta-10" || *disp.begun.Men != 4 || *disp.begun.Women != 5 || *disp.begun.Companions != 3 || disp.begun.DriverID != "CH-001" {
		t.Fatalf("begin: %+v", disp.begun)
	}
	if b := send.last(); len(b.rows) != 4 || !strings.Contains(b.body, "Ruta iniciada a las 07:30") {
		t.Fatalf("started (local time 07:30): %+v", b)
	}
	if store.sessions[driverPhone].ShiftID != 7 {
		t.Fatalf("session shift: %+v", store.sessions[driverPhone])
	}

	// A rider asks for the ramp: the driver gets a button and acknowledges.
	if n := bot.NotifyRamp(context.Background(), live.RampRequest{ID: 5, RouteID: "ruta-10", StopName: "Hospital"}); n != 1 {
		t.Fatalf("notified %d", n)
	}
	tap(bot, "ack:5")
	if disp.acked != 5 || !strings.Contains(send.last().body, "Listo") {
		t.Fatalf("ack: %d %q", disp.acked, send.last().body)
	}
	tap(bot, "ack:99")
	if !strings.Contains(send.last().body, "ya no está pendiente") {
		t.Fatalf("stale ack: %q", send.last().body)
	}

	// Ramp failure: reported, the route goes on.
	tap(bot, "ramp")
	tap(bot, "yes")
	if len(disp.reported) != 1 || disp.reported[0] != "ramp_failure" || !strings.Contains(send.last().body, "Sigues en ruta") {
		t.Fatalf("ramp: %v %q", disp.reported, send.last().body)
	}
	// Changing their mind reports nothing.
	say(bot, "se descompuso el camion")
	tap(bot, "cancel")
	if len(disp.reported) != 1 {
		t.Fatalf("cancelled report was sent: %v", disp.reported)
	}

	say(bot, "ya terminé")
	tap(bot, "final:depot")
	if disp.finished != "depot" || !strings.Contains(send.last().body, "Inicio: 07:30 · Fin: 09:05 (1 h 35 min)") {
		t.Fatalf("end: %q %q", disp.finished, send.last().body)
	}
	if s := store.sessions[driverPhone]; s.Step != stepIdle || s.ShiftID != 0 {
		t.Fatalf("after end: %+v", s)
	}
}

func TestBreakdownEndsTheRoute(t *testing.T) {
	bot, store, disp, send := newBot()
	store.open = &OpenShift{ID: 3, BusNumber: "22", RouteID: "ruta-2", StartedAt: time.Now()}
	say(bot, "hola") // a route opened elsewhere (the app) is picked up
	if len(send.last().rows) != 4 {
		t.Fatalf("route menu: %+v", send.last())
	}
	tap(bot, "breakdown")
	tap(bot, "yes")
	if disp.reported[0] != "breakdown" || !strings.Contains(send.last().body, "La ruta se canceló") {
		t.Fatalf("%v %q", disp.reported, send.last().body)
	}
}

func TestRouteClosedByTheSystemResetsTheChat(t *testing.T) {
	bot, store, _, send := newBot()
	store.sessions[driverPhone] = Session{Phone: driverPhone, DriverID: "CH-001", Step: stepOnRoute, ShiftID: 3}
	say(bot, "fin")
	if !strings.Contains(send.out[0].body, "ya estaba cerrada") {
		t.Fatalf("%+v", send.out)
	}
}

func TestInactiveDriver(t *testing.T) {
	bot, store, _, send := newBot()
	store.active = false
	say(bot, "inicio")
	if !strings.Contains(send.last().body, "desactivada") {
		t.Fatalf("%q", send.last().body)
	}
}

func TestWebhookChecksSignatureAndToken(t *testing.T) {
	bot, _, _, send := newBot()
	h := &Webhook{Bot: bot, VerifyToken: "token-de-verificacion", AppSecret: "secreto", Ctx: context.Background()}

	rec := httptest.NewRecorder()
	h.Verify(rec, httptest.NewRequest("GET", "/?hub.mode=subscribe&hub.verify_token=token-de-verificacion&hub.challenge=123", nil))
	if rec.Code != 200 || rec.Body.String() != "123" {
		t.Fatalf("verify: %d %q", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.Verify(rec, httptest.NewRequest("GET", "/?hub.mode=subscribe&hub.verify_token=otro&hub.challenge=123", nil))
	if rec.Code != 403 {
		t.Fatalf("wrong token: %d", rec.Code)
	}

	body := `{"entry":[{"changes":[{"value":{"messages":[{"from":"` + driverPhone + `","id":"wamid.1","type":"text","text":{"body":"hola"}}]}}]}]}`
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256=00")
	h.Receive(rec, req)
	if rec.Code != 403 {
		t.Fatalf("bad signature: %d", rec.Code)
	}

	mac := hmac.New(sha256.New, []byte("secreto"))
	mac.Write([]byte(body))
	for range 2 { // the second delivery of the same message is ignored
		rec = httptest.NewRecorder()
		req = httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		h.Receive(rec, req)
		if rec.Code != 200 {
			t.Fatalf("signed: %d", rec.Code)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for send.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := send.count(); n != 1 {
		t.Fatalf("want exactly one reply, got %d", n)
	}
}

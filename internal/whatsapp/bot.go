package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/eter/rutacree-backend/internal/dispatch"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/live"
)

// Dispatcher opens, reports and closes routes: *dispatch.Service.
type Dispatcher interface {
	Begin(ctx context.Context, req dispatch.StartRequest) (dispatch.Started, error)
	Report(ctx context.Context, shiftID uint64, kind string) (uint64, error)
	Finish(ctx context.Context, shiftID uint64, finalLocation string) (time.Time, time.Time, error)
	Acknowledge(ctx context.Context, shiftID, requestID uint64) error
}

// RouteInfo is a route the driver can pick.
type RouteInfo struct{ ID, Name string }

// Incoming is one message from a driver: typed text, or the id of a tapped button / list option.
type Incoming struct {
	ID    string
	From  string // WhatsApp id: country code + number, digits only
	Text  string
	Reply string
}

// Conversation steps.
const (
	stepIdle            = ""
	stepBus             = "bus"
	stepRoute           = "route"
	stepConfirmStart    = "confirm_start"
	stepOnRoute         = "on_route"
	stepConfirmRamp     = "confirm_ramp"
	stepConfirmFailure  = "confirm_breakdown"
	stepConfirmAccident = "confirm_accident"
	stepFinal           = "final"
)

const maxPassengers = 99

// countQuestion asks for one of the trip's user counts, in the order of countQuestions.
type countQuestion struct {
	step, ask, label string
	field            func(*Draft) **int
}

// The DIF's weekly log: men and women (the total), then which of them are people with a
// disability, older adults, pregnant women and companions.
var countQuestions = []countQuestion{
	{"men", "¿Cuántos *hombres* suben? Escribe un número (0 si ninguno).", "Hombres", func(d *Draft) **int { return &d.Men }},
	{"women", "¿Cuántas *mujeres* suben?", "Mujeres", func(d *Draft) **int { return &d.Women }},
	{"disability", "De ellos, ¿cuántas personas con *discapacidad*?", "Discapacidad", func(d *Draft) **int { return &d.Disability }},
	{"elderly", "¿Cuántos *adultos mayores*?", "Adultos mayores", func(d *Draft) **int { return &d.Elderly }},
	{"pregnant", "¿Cuántas *embarazadas*?", "Embarazadas", func(d *Draft) **int { return &d.Pregnant }},
	{"companions", "¿Cuántos *acompañantes*?", "Acompañantes", func(d *Draft) **int { return &d.Companions }},
}

func countStep(step string) int {
	for i, q := range countQuestions {
		if q.step == step {
			return i
		}
	}
	return -1
}

// usersSummary is the counts as the driver confirms them.
func usersSummary(d Draft) string {
	parts := make([]string, len(countQuestions))
	for i, q := range countQuestions {
		parts[i] = fmt.Sprintf("%s: %d", q.label, deref(*q.field(&d)))
	}
	return fmt.Sprintf("%s · %s (total %d)\n%s", parts[0], parts[1], deref(d.Men)+deref(d.Women), strings.Join(parts[2:], " · "))
}

type Bot struct {
	Store    Store
	Dispatch Dispatcher
	Send     Sender
	Routes   func() []RouteInfo
	// Local time zone for the times shown to drivers.
	Location *time.Location

	locks sync.Map // phone -> *sync.Mutex: one message at a time per chat, in order
}

// Handle answers one message. Errors are logged; the driver always gets some reply.
func (b *Bot) Handle(ctx context.Context, in Incoming) {
	mu, _ := b.locks.LoadOrStore(in.From, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if err := b.handle(ctx, in); err != nil {
		slog.Error("bot de WhatsApp", "de", maskPhone(in.From), "error", err)
		_ = b.Send.Text(ctx, in.From, "No pude completar eso por un problema del sistema. Intenta de nuevo en un minuto.")
	}
}

func (b *Bot) handle(ctx context.Context, in Incoming) error {
	to := in.From
	driver, n, err := b.Store.DriverByPhone(ctx, last10(to))
	if err != nil {
		return err
	}
	switch {
	case n == 0:
		return b.Send.Text(ctx, to, "Hola 👋 Este es el WhatsApp de los choferes de RutaCree y tu número no está registrado.\n\n"+
			"Pide al despacho que agregue este teléfono a tu cuenta de chofer en el panel.")
	case n > 1:
		return b.Send.Text(ctx, to, "Tu número está registrado en más de un chofer. Pide al despacho que lo corrija en el panel.")
	case !driver.Active:
		return b.Send.Text(ctx, to, "Tu cuenta de chofer está desactivada. Habla con el despacho.")
	}

	sess, err := b.Store.Session(ctx, to)
	if err != nil {
		return err
	}
	if sess.DriverID != driver.ID {
		sess = Session{Phone: to, DriverID: driver.ID}
	}
	// The database says whether a route is open (it may have been opened in the app, or closed by
	// the system or the panel); the chat follows it.
	open, onRoute, err := b.Store.OpenShiftOf(ctx, driver.ID)
	if err != nil {
		return err
	}
	if onRoute {
		sess.ShiftID = open.ID
	} else {
		sess.ShiftID = 0
	}
	if !onRoute && routeSteps[sess.Step] {
		sess.Step = stepIdle
		if err := b.Send.Text(ctx, to, "Tu ruta ya estaba cerrada (venció o la cerró el despacho)."); err != nil {
			return err
		}
	}
	if onRoute && !routeSteps[sess.Step] {
		sess.Step = stepOnRoute
	}

	err = b.step(ctx, &sess, driver, open, in)
	if serr := b.Store.SaveSession(ctx, sess); err == nil {
		err = serr
	}
	return err
}

var routeSteps = map[string]bool{stepOnRoute: true, stepConfirmRamp: true, stepConfirmFailure: true, stepConfirmAccident: true, stepFinal: true}

func (b *Bot) step(ctx context.Context, sess *Session, driver Driver, open OpenShift, in Incoming) error {
	to := in.From
	word := normalize(in.Text)
	choice := in.Reply
	if choice == "" {
		choice = word
	}

	if id, ok := strings.CutPrefix(in.Reply, "ack:"); ok {
		return b.acknowledge(ctx, sess, to, id)
	}
	if isAny(word, "cancelar", "salir") || choice == "cancel" {
		sess.Draft = Draft{}
		if sess.ShiftID != 0 {
			sess.Step = stepOnRoute
			return b.routeMenu(ctx, to, open, "Cancelado. Sigues en ruta.")
		}
		sess.Step = stepIdle
		return b.idleMenu(ctx, to, driver, "Cancelado.")
	}

	switch sess.Step {
	case stepIdle:
		if choice == "start" || hasAny(word, "inicio", "iniciar", "empezar", "comenzar") {
			sess.Draft = Draft{}
			sess.Step = stepBus
			return b.Send.Text(ctx, to, "🚌 *Iniciar ruta*\n\n¿Qué *unidad* manejas hoy? Escribe el número económico (ej. 25-3369).\n\n_Escribe «cancelar» en cualquier momento para salir._")
		}
		return b.idleMenu(ctx, to, driver, "")

	case stepBus:
		bus := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(in.Text), " ", ""))
		bus = strings.TrimPrefix(strings.TrimPrefix(bus, "UNIDAD"), "#")
		if !dispatch.BusNumberPattern.MatchString(bus) {
			return b.Send.Text(ctx, to, "Escribe solo el número económico de la unidad (ej. 25-3369).")
		}
		status, err := b.Store.BusStatus(ctx, bus)
		if err != nil {
			return err
		}
		switch status {
		case "":
			return b.Send.Text(ctx, to, fmt.Sprintf("No encuentro la unidad *%s* en la flotilla. Revisa el número económico y escríbelo otra vez.", bus))
		case "workshop":
			return b.Send.Text(ctx, to, fmt.Sprintf("La unidad *%s* está en el taller. Escribe otra unidad o avisa al despacho.", bus))
		case "retired":
			return b.Send.Text(ctx, to, fmt.Sprintf("La unidad *%s* está dada de baja. Revisa el número económico (ej. 25-3369).", bus))
		}
		sess.Draft.Bus = bus
		sess.Step = stepRoute
		return b.askRoute(ctx, to)

	case stepRoute:
		route, ok := b.matchRoute(in.Text)
		if !ok {
			return b.askRoute(ctx, to)
		}
		sess.Draft.RouteID = route.ID
		sess.Step = countQuestions[0].step
		return b.Send.Text(ctx, to, countQuestions[0].ask)

	case countQuestions[0].step, countQuestions[1].step, countQuestions[2].step, countQuestions[3].step,
		countQuestions[4].step, countQuestions[5].step:
		n, ok := count(in.Text)
		if !ok {
			return b.Send.Text(ctx, to, fmt.Sprintf("Escribe solo un número del 0 al %d.", maxPassengers))
		}
		i := countStep(sess.Step)
		*countQuestions[i].field(&sess.Draft) = &n
		if i+1 < len(countQuestions) {
			sess.Step = countQuestions[i+1].step
			return b.Send.Text(ctx, to, countQuestions[i+1].ask)
		}
		sess.Step = stepConfirmStart
		d := sess.Draft
		return b.Send.Buttons(ctx, to, fmt.Sprintf("Revisa los datos:\n\nUnidad: *%s*\nRuta: *%s*\n%s",
			d.Bus, b.routeName(d.RouteID), usersSummary(d)),
			[]Button{{"ok", "✅ Iniciar ruta"}, {"redo", "✏️ Corregir"}, {"cancel", "Cancelar"}})

	case stepConfirmStart:
		switch {
		case choice == "redo" || hasAny(word, "corregir"):
			sess.Draft = Draft{}
			sess.Step = stepBus
			return b.Send.Text(ctx, to, "Va de nuevo. ¿Qué *unidad* manejas hoy?")
		case choice == "ok" || isAny(word, "si", "ok", "iniciar", "confirmar"):
			return b.begin(ctx, sess, driver, to)
		}
		return b.Send.Buttons(ctx, to, "¿Inicio la ruta con esos datos?",
			[]Button{{"ok", "✅ Iniciar ruta"}, {"redo", "✏️ Corregir"}, {"cancel", "Cancelar"}})

	case stepOnRoute:
		switch {
		case choice == "ramp" || hasAny(word, "rampa"):
			sess.Step = stepConfirmRamp
			return b.Send.Buttons(ctx, to, "♿ ¿Reportar que la *rampa no funciona*?\n\nLa unidad sigue su ruta, a los pasajeros se les avisa que no hay rampa y el taller la revisa.",
				[]Button{{"yes", "Sí, reportar"}, {"cancel", "No"}})
		case choice == "breakdown" || hasAny(word, "falla", "camion", "descompuso", "descompuesto", "averia", "motor", "llanta"):
			sess.Step = stepConfirmFailure
			return b.Send.Buttons(ctx, to, "⚠️ ¿Reportar *falla del camión*?\n\nSe cancela la ruta y se avisa a los pasajeros.",
				[]Button{{"yes", "Sí, reportar"}, {"cancel", "No"}})
		case choice == "accident" || hasAny(word, "choque", "accidente", "choco"):
			sess.Step = stepConfirmAccident
			return b.Send.Buttons(ctx, to, "🚨 ¿Reportar *choque o accidente*?\n\nSe cancela la ruta y se avisa a los pasajeros.",
				[]Button{{"yes", "Sí, reportar"}, {"cancel", "No"}})
		case choice == "end" || hasAny(word, "fin", "terminar", "termine", "acabe", "final"):
			sess.Step = stepFinal
			return b.Send.Buttons(ctx, to, "🏁 ¿Dónde queda la unidad?",
				[]Button{{"final:terminal", "Terminal"}, {"final:workshop", "Taller"}, {"final:depot", "Encierro"}})
		}
		return b.routeMenu(ctx, to, open, "")

	case stepConfirmRamp, stepConfirmFailure, stepConfirmAccident:
		if !(choice == "yes" || isAny(word, "si", "reportar")) {
			sess.Step = stepOnRoute
			return b.routeMenu(ctx, to, open, "No se reportó nada.")
		}
		kind := map[string]string{stepConfirmRamp: "ramp_failure", stepConfirmFailure: "breakdown", stepConfirmAccident: "accident"}[sess.Step]
		if _, err := b.Dispatch.Report(ctx, sess.ShiftID, kind); err != nil {
			sess.Step = stepOnRoute
			return b.fail(ctx, to, err)
		}
		now := b.clock(time.Now())
		if kind == "ramp_failure" {
			sess.Step = stepOnRoute
			return b.routeMenu(ctx, to, open, fmt.Sprintf("✅ Falla de rampa registrada a las %s. La unidad queda marcada sin rampa y se avisó a los pasajeros. Sigues en ruta.", now))
		}
		sess.Step, sess.ShiftID = stepIdle, 0
		what := map[string]string{"breakdown": "Falla del camión", "accident": "Choque"}[kind]
		return b.idleMenu(ctx, to, driver, fmt.Sprintf("✅ %s registrada a las %s. La ruta se canceló y se avisó a los pasajeros. Si hay lesionados llama al 911.", what, now))

	case stepFinal:
		loc, ok := strings.CutPrefix(choice, "final:")
		if !ok {
			loc = map[string]string{"terminal": "terminal", "taller": "workshop", "encierro": "depot"}[word]
		}
		if loc != "terminal" && loc != "workshop" && loc != "depot" {
			return b.Send.Buttons(ctx, to, "Elige dónde queda la unidad:",
				[]Button{{"final:terminal", "Terminal"}, {"final:workshop", "Taller"}, {"final:depot", "Encierro"}})
		}
		started, ended, err := b.Dispatch.Finish(ctx, sess.ShiftID, loc)
		if err != nil {
			sess.Step = stepOnRoute
			return b.fail(ctx, to, err)
		}
		sess.Step, sess.ShiftID = stepIdle, 0
		return b.idleMenu(ctx, to, driver, fmt.Sprintf("🏁 *Ruta terminada*\nInicio: %s · Fin: %s (%s)\nUnidad %s · %s\n\n¡Gracias!",
			b.clock(started), b.clock(ended), duration(ended.Sub(started)), open.BusNumber, b.routeName(open.RouteID)))
	}

	sess.Step = stepIdle
	return b.idleMenu(ctx, to, driver, "")
}

func (b *Bot) begin(ctx context.Context, sess *Session, driver Driver, to string) error {
	d := sess.Draft
	st, err := b.Dispatch.Begin(ctx, dispatch.StartRequest{
		BusNumber: d.Bus, RouteID: d.RouteID, Men: d.Men, Women: d.Women, Disability: d.Disability, Elderly: d.Elderly,
		Pregnant: d.Pregnant, Companions: d.Companions, DriverID: driver.ID,
	})
	if err != nil {
		sess.Step = stepIdle
		return b.fail(ctx, to, err)
	}
	sess.Step, sess.ShiftID, sess.Draft = stepOnRoute, st.Shift.ID, Draft{}
	open := OpenShift{ID: st.Shift.ID, BusNumber: st.Shift.BusNumber, RouteID: st.Shift.RouteID, StartedAt: st.StartedAt}
	return b.routeMenu(ctx, to, open, fmt.Sprintf("✅ *Ruta iniciada a las %s*\nUnidad %s · %s\n%s\n\n"+
		"Aquí te llegarán las solicitudes de rampa. ¡Buen viaje!",
		b.clock(st.StartedAt), st.Shift.BusNumber, b.routeName(st.Shift.RouteID), usersSummary(d)))
}

func (b *Bot) acknowledge(ctx context.Context, sess *Session, to, id string) error {
	reqID, err := strconv.ParseUint(id, 10, 64)
	if err != nil || sess.ShiftID == 0 {
		return b.Send.Text(ctx, to, "Esa solicitud ya no está pendiente.")
	}
	if err := b.Dispatch.Acknowledge(ctx, sess.ShiftID, reqID); err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return b.Send.Text(ctx, to, "Esa solicitud ya no está pendiente (otro chofer la atendió o ya venció).")
		}
		return err
	}
	return b.Send.Text(ctx, to, "👍 Listo, el pasajero ya sabe que viste su solicitud.")
}

// NotifyRamp sends a rider's ramp request to every driver on that route who uses the chat.
func (b *Bot) NotifyRamp(ctx context.Context, req live.RampRequest) int {
	phones, err := b.Store.PhonesOnRoute(ctx, req.RouteID)
	if err != nil {
		slog.Error("bot de WhatsApp: choferes en ruta", "error", err)
		return 0
	}
	for _, p := range phones {
		go func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cancel()
			if err := b.Send.Buttons(sctx, p, fmt.Sprintf("♿ *Solicitud de rampa*\nParada: *%s*\n\nUn pasajero te espera para subir con la rampa.", req.StopName),
				[]Button{{fmt.Sprintf("ack:%d", req.ID), "👍 Enterado"}}); err != nil {
				slog.Error("bot de WhatsApp: aviso de rampa", "a", maskPhone(p), "error", err)
			}
		}()
	}
	return len(phones)
}

func (b *Bot) idleMenu(ctx context.Context, to string, driver Driver, prefix string) error {
	body := fmt.Sprintf("Hola, %s 👋\nNo tienes una ruta en curso.", firstName(driver.Name))
	if prefix != "" {
		body = prefix + "\n\n¿Inicias otra ruta?"
	}
	return b.Send.Buttons(ctx, to, body, []Button{{"start", "🚌 Iniciar ruta"}})
}

func (b *Bot) routeMenu(ctx context.Context, to string, open OpenShift, prefix string) error {
	body := fmt.Sprintf("Unidad %s · %s\nEn ruta desde las %s.\n\n¿Qué necesitas reportar?", open.BusNumber, b.routeName(open.RouteID), b.clock(open.StartedAt))
	if prefix != "" {
		body = prefix + "\n\n" + body
	}
	return b.Send.List(ctx, to, body, "Reportar", []Row{
		{"ramp", "♿ Falla de rampa", "La unidad sigue su ruta sin rampa"},
		{"breakdown", "⚠️ Falla del camión", "Se cancela la ruta"},
		{"accident", "🚨 Choque o accidente", "Se cancela la ruta"},
		{"end", "🏁 Terminar ruta", "Registra la hora de fin"},
	})
}

func (b *Bot) askRoute(ctx context.Context, to string) error {
	var sb strings.Builder
	sb.WriteString("¿Qué *ruta* haces? Escribe el número:\n")
	for _, r := range b.Routes() {
		sb.WriteString("\n" + r.Name)
	}
	return b.Send.Text(ctx, to, sb.String())
}

var digits = regexp.MustCompile(`\d+`)

// matchRoute accepts "2", "ruta 2", "Ruta 2 - Centro" or the route id.
func (b *Bot) matchRoute(text string) (RouteInfo, bool) {
	t := normalize(text)
	routes := b.Routes()
	for _, r := range routes {
		if t == strings.ToLower(r.ID) || t == normalize(r.Name) {
			return r, true
		}
	}
	num := digits.FindString(t)
	if num == "" {
		return RouteInfo{}, false
	}
	for _, r := range routes {
		if digits.FindString(r.Name) == num || strings.TrimPrefix(r.ID, "ruta-") == num {
			return r, true
		}
	}
	return RouteInfo{}, false
}

func (b *Bot) routeName(id string) string {
	for _, r := range b.Routes() {
		if r.ID == id {
			return r.Name
		}
	}
	return id
}

func (b *Bot) fail(ctx context.Context, to string, err error) error {
	var he *httpx.Error
	if errors.As(err, &he) {
		return b.Send.Text(ctx, to, "❌ "+he.Message)
	}
	return err
}

func (b *Bot) clock(t time.Time) string {
	loc := b.Location
	if loc == nil {
		loc = time.Local
	}
	return t.In(loc).Format("15:04")
}

func duration(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %02d min", m/60, m%60)
}

func count(text string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return n, err == nil && n >= 0 && n <= maxPassengers
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

var accents = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u")

// normalize lowercases, trims and drops accents: "Terminé" → "termine".
func normalize(s string) string { return accents.Replace(strings.ToLower(strings.TrimSpace(s))) }

func isAny(word string, options ...string) bool {
	for _, o := range options {
		if word == o {
			return true
		}
	}
	return false
}

func hasAny(word string, options ...string) bool {
	for _, f := range strings.FieldsFunc(word, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if isAny(f, options...) {
			return true
		}
	}
	return false
}

func firstName(name string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	return name
}

func last10(phone string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, phone)
	if len(d) > 10 {
		return d[len(d)-10:]
	}
	return d
}

// maskPhone keeps logs useful without storing full numbers: "52161…92".
func maskPhone(p string) string {
	if len(p) < 6 {
		return "…"
	}
	return p[:5] + "…" + p[len(p)-2:]
}

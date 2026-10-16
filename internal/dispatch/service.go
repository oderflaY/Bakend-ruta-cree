// Package dispatch is the backend side of what the app simulates in SimulatedDispatch: driver
// shifts and positions, incidents, ramp requests and rider reports.
package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/live"
	"github.com/eter/rutacree-backend/internal/transit"
)

var (
	BusNumberPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,15}$`)
	DriverIDPattern  = regexp.MustCompile(`^[A-Z0-9-]{1,16}$`)
)

const rampRequestTTL = 15 * time.Minute

// While the phone's tracker (Traccar Client) delivered a fix this recently, positions sent by the
// RutaCree app are ignored: two sources for one bus would make the pin jitter. The app is the fallback.
const trackerPriority = time.Minute

// Tracker is the Traccar bridge, as seen by dispatch.
type Tracker interface {
	EnsureDevice(ctx context.Context, busNumber string) error
	LastFix(busNumber string) (time.Time, bool)
	Connected() bool
	PublicURL() string
}

type Service struct {
	DB       *sql.DB
	Hub      *live.Hub
	Catalog  *transit.Catalog
	Tokens   *auth.Tokens
	Recorder *live.Recorder
	TokenTTL time.Duration
	// Nil when Traccar is not configured: the app's own GPS reports are the only source.
	Tracker Tracker
	// Dispatch's WhatsApp number, where the driver's app sends route start/end and failures.
	WhatsApp string
	// Also told about every new ramp request (the WhatsApp bot); returns how many drivers it reached.
	OnRamp func(live.RampRequest) int
	// Local time zone of the forms (the vehicle report's day and times); nil is time.Local.
	Location *time.Location
}

// LoadActiveShifts restores open shifts into the hub after a restart.
func (s *Service) LoadActiveShifts(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT s.id, s.driver_id, s.bus_id, b.number, s.route_id, b.ramp_operational
		FROM shifts s JOIN buses b ON b.id = s.bus_id WHERE s.ended_at IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var sh live.Shift
		if err := rows.Scan(&sh.ID, &sh.DriverID, &sh.BusID, &sh.BusNumber, &sh.RouteID, &sh.Ramp); err != nil {
			return err
		}
		s.Hub.AddShift(sh)
		n++
	}
	slog.Info("turnos activos restaurados", "n", n)
	return rows.Err()
}

// ---- shifts ----

// Most passengers a bus can carry; counts above this are typing mistakes.
const maxPassengers = 99

type startShiftReq struct {
	BusNumber string `json:"busNumber"`
	RouteID   string `json:"routeId"`
	// Users of this trip (vuelta), as the DIF's weekly log counts them. Optional for older apps,
	// which send children/adults/women instead.
	Disability *int   `json:"disability"`
	Elderly    *int   `json:"elderly"`
	Pregnant   *int   `json:"pregnant"`
	Companions *int   `json:"companions"`
	Men        *int   `json:"men"`
	Women      *int   `json:"women"`
	Children   *int   `json:"children"`
	Adults     *int   `json:"adults"`
	DriverID   string `json:"-"`
}

func (r startShiftReq) counts() []*int {
	return []*int{r.Disability, r.Elderly, r.Pregnant, r.Companions, r.Men, r.Women, r.Children, r.Adults}
}

func validCount(n *int) bool { return n == nil || (*n >= 0 && *n <= maxPassengers) }

type startShiftResp struct {
	Token           string    `json:"token"`
	ExpiresAt       time.Time `json:"expiresAt"`
	ShiftID         uint64    `json:"shiftId"`
	BusID           uint64    `json:"busId"`
	BusNumber       string    `json:"busNumber"`
	RouteID         string    `json:"routeId"`
	DriverName      string    `json:"driverName"`
	RampOperational bool      `json:"rampOperational"`
	// "traccar" when the bus position should come from Traccar Client on the driver's phone.
	PositionSource string `json:"positionSource"`
	// What to type in Traccar Client: device identifier (the bus number) and server URL.
	TrackerDeviceID string    `json:"trackerDeviceId,omitempty"`
	TrackerURL      string    `json:"trackerUrl,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
	// Dispatch's WhatsApp (digits only); empty lets the driver pick the chat.
	WhatsApp string `json:"whatsapp"`
}

// StartShift is POST /api/v1/driver/shifts {"busNumber", "routeId"} with the driver's login token
// (driver_account): opens a shift on a bus and route and returns a token bound to that shift.
func (s *Service) StartShift(w http.ResponseWriter, r *http.Request) error {
	var req startShiftReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	// The driver signed in with email and password (driver_account token).
	req.DriverID = auth.FromContext(r.Context()).Subject
	st, err := s.Begin(r.Context(), req)
	if err != nil {
		return err
	}
	shift := st.Shift
	token, exp, err := s.Tokens.Issue(auth.Claims{
		Role: auth.RoleDriver, ShiftID: shift.ID, BusID: shift.BusID, RouteID: shift.RouteID,
		RegisteredClaims: auth.Subject(req.DriverID),
	}, s.TokenTTL)
	if err != nil {
		return err
	}
	resp := startShiftResp{
		Token: token, ExpiresAt: exp, ShiftID: shift.ID, BusID: shift.BusID, BusNumber: shift.BusNumber,
		RouteID: shift.RouteID, DriverName: st.DriverName, RampOperational: shift.Ramp, PositionSource: "app",
		StartedAt: st.StartedAt, WhatsApp: s.WhatsApp,
	}
	if s.Tracker != nil {
		resp.PositionSource = "traccar"
		resp.TrackerDeviceID = shift.BusNumber
		resp.TrackerURL = s.Tracker.PublicURL()
	}
	httpx.WriteJSON(w, http.StatusCreated, resp)
	return nil
}

// Started is a shift just opened by Begin.
type Started struct {
	Shift      live.Shift
	StartedAt  time.Time
	DriverName string
}

// StartRequest is what opening a shift needs, from the app or from the WhatsApp bot.
type StartRequest = startShiftReq

// Begin validates and opens a shift for an active driver: the core of StartShift, shared with the
// WhatsApp bot. Errors are *httpx.Error with a message fit to show the driver.
func (s *Service) Begin(ctx context.Context, req startShiftReq) (Started, error) {
	req.BusNumber = strings.ToUpper(strings.TrimSpace(req.BusNumber))
	if !BusNumberPattern.MatchString(req.BusNumber) {
		return Started{}, httpx.BadRequest("Número de unidad inválido (hasta 16 letras, números o guiones)")
	}
	var busStatus string
	switch err := s.DB.QueryRowContext(ctx, `SELECT status FROM buses WHERE number = ?`, req.BusNumber).Scan(&busStatus); {
	case err == nil && busStatus == "workshop":
		return Started{}, httpx.NewError(http.StatusConflict, "bus_in_workshop", "La unidad está en el taller; avisa al despacho")
	case err == nil && busStatus == "retired":
		return Started{}, httpx.NewError(http.StatusConflict, "bus_retired", "Esa unidad está dada de baja; revisa el número económico")
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return Started{}, err
	}
	if _, ok := s.Catalog.Route(req.RouteID); !ok {
		return Started{}, httpx.BadRequest("Ruta desconocida")
	}
	for _, n := range req.counts() {
		if !validCount(n) {
			return Started{}, httpx.BadRequest(fmt.Sprintf("Las cantidades de pasajeros van de 0 a %d", maxPassengers))
		}
	}

	// Re-check the driver is still active.
	var (
		name   string
		active bool
	)
	err := s.DB.QueryRowContext(ctx, `SELECT name, active FROM drivers WHERE id = ?`, req.DriverID).Scan(&name, &active)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !active) {
		return Started{}, httpx.ErrUnauthorized
	}
	if err != nil {
		return Started{}, err
	}

	shift, startedAt, replaced, err := s.openShift(ctx, req)
	if err != nil {
		return Started{}, err
	}
	for _, id := range replaced {
		s.Hub.RemoveShift(id)
	}
	s.Hub.AddShift(shift)
	s.RegisterDevice(ctx, shift.BusNumber)
	slog.Info("turno iniciado", "turno", shift.ID, "chofer", req.DriverID, "unidad", req.BusNumber, "ruta", req.RouteID,
		"discapacidad", req.Disability, "adultos_mayores", req.Elderly, "embarazadas", req.Pregnant,
		"acompanantes", req.Companions, "hombres", req.Men, "mujeres", req.Women)
	return Started{Shift: shift, StartedAt: startedAt, DriverName: name}, nil
}

// openShift closes any open shift of this driver or this bus ("replaced") and opens a new one.
func (s *Service) openShift(ctx context.Context, req startShiftReq) (live.Shift, time.Time, []uint64, error) {
	var startedAt time.Time
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return live.Shift{}, startedAt, nil, err
	}
	defer tx.Rollback()
	driverID, busNumber, routeID := req.DriverID, req.BusNumber, req.RouteID

	sh := live.Shift{DriverID: driverID, BusNumber: busNumber, RouteID: routeID}
	res, err := tx.ExecContext(ctx, `INSERT INTO buses (number) VALUES (?) ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`, busNumber)
	if err != nil {
		return sh, startedAt, nil, err
	}
	busID, err := res.LastInsertId()
	if err != nil {
		return sh, startedAt, nil, err
	}
	sh.BusID = uint64(busID)
	if err := tx.QueryRowContext(ctx, `SELECT ramp_operational FROM buses WHERE id = ?`, sh.BusID).Scan(&sh.Ramp); err != nil {
		return sh, startedAt, nil, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT id FROM shifts WHERE ended_at IS NULL AND (driver_id = ? OR bus_id = ?) FOR UPDATE`,
		driverID, sh.BusID)
	if err != nil {
		return sh, startedAt, nil, err
	}
	var replaced []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return sh, startedAt, nil, err
		}
		replaced = append(replaced, id)
	}
	rows.Close()
	if len(replaced) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE shifts SET ended_at = CURRENT_TIMESTAMP(3), end_reason = 'replaced'
			WHERE ended_at IS NULL AND (driver_id = ? OR bus_id = ?)`, driverID, sh.BusID); err != nil {
			return sh, startedAt, nil, err
		}
	}

	res, err = tx.ExecContext(ctx, `INSERT INTO shifts (driver_id, bus_id, route_id, disability, elderly, pregnant,
		companions, men, women, children, adults) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		driverID, sh.BusID, routeID, req.Disability, req.Elderly, req.Pregnant, req.Companions, req.Men, req.Women,
		req.Children, req.Adults)
	if err != nil {
		return sh, startedAt, nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return sh, startedAt, nil, err
	}
	sh.ID = uint64(id)
	if err := tx.QueryRowContext(ctx, `SELECT started_at FROM shifts WHERE id = ?`, sh.ID).Scan(&startedAt); err != nil {
		return sh, startedAt, nil, err
	}
	return sh, startedAt.UTC(), replaced, tx.Commit()
}

var finalLocations = map[string]bool{"terminal": true, "workshop": true, "depot": true}

// EndShift is POST /api/v1/driver/shifts/end {"finalLocation": "terminal"|"workshop"|"depot"} and
// answers with the times recorded for the route: {"startedAt", "endedAt"}.
func (s *Service) EndShift(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		FinalLocation string `json:"finalLocation"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if !finalLocations[req.FinalLocation] {
		return httpx.BadRequest("finalLocation debe ser terminal, workshop o depot")
	}
	started, ended, err := s.Finish(r.Context(), auth.FromContext(r.Context()).ShiftID, req.FinalLocation)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"startedAt": started, "endedAt": ended})
	return nil
}

// Finish closes a shift as completed and returns when it started and ended (UTC).
func (s *Service) Finish(ctx context.Context, shiftID uint64, finalLocation string) (started, ended time.Time, err error) {
	if err := s.closeShift(ctx, shiftID, "completed", finalLocation); err != nil {
		return started, ended, err
	}
	var st, en sql.NullTime
	if err := s.DB.QueryRowContext(ctx, `SELECT started_at, ended_at FROM shifts WHERE id = ?`, shiftID).Scan(&st, &en); err != nil {
		return started, ended, err
	}
	return st.Time.UTC(), en.Time.UTC(), nil
}

func (s *Service) closeShift(ctx context.Context, shiftID uint64, reason, finalLocation string) error {
	var loc any
	if finalLocation != "" {
		loc = finalLocation
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE shifts SET ended_at = CURRENT_TIMESTAMP(3), end_reason = ?, final_location = ?
		WHERE id = ? AND ended_at IS NULL`, reason, loc, shiftID); err != nil {
		return err
	}
	s.Hub.RemoveShift(shiftID)
	slog.Info("turno cerrado", "turno", shiftID, "motivo", reason)
	return nil
}

// CloseDriverShifts ends every open shift of a driver (used when an admin deactivates them).
func (s *Service) CloseDriverShifts(ctx context.Context, driverID string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM shifts WHERE driver_id = ? AND ended_at IS NULL`, driverID)
	if err != nil {
		return err
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.closeShift(ctx, id, "expired", ""); err != nil {
			return err
		}
	}
	return nil
}

// RegisterDevice adds the bus to Traccar so its GPS is accepted from the first report. Best effort:
// with Traccar down the bus still works (and it is retried when a shift starts).
func (s *Service) RegisterDevice(ctx context.Context, busNumber string) {
	if s.Tracker == nil {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Tracker.EnsureDevice(tctx, busNumber); err != nil {
		slog.Warn("no se registró la unidad en Traccar", "unidad", busNumber, "error", err)
	}
}

// CloseBusShifts ends the open shift of a bus sent to the workshop: it disappears from the riders' map.
func (s *Service) CloseBusShifts(ctx context.Context, busID uint64) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM shifts WHERE bus_id = ? AND ended_at IS NULL`, busID)
	if err != nil {
		return err
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := s.closeShift(ctx, id, "withdrawn", "workshop"); err != nil {
			return err
		}
	}
	return nil
}

// ---- positions ----

type positionReq struct {
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	SpeedKmh float64 `json:"speedKmh"`
}

// ReportPosition is POST /api/v1/driver/positions.
func (s *Service) ReportPosition(w http.ResponseWriter, r *http.Request) error {
	var req positionReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := s.acceptPosition(auth.FromContext(r.Context()), req); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) acceptPosition(c *auth.Claims, p positionReq) error {
	if p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon > 180 || (p.Lat == 0 && p.Lon == 0) {
		return httpx.BadRequest("Coordenadas inválidas")
	}
	if p.SpeedKmh < 0 || p.SpeedKmh > 200 || p.SpeedKmh != p.SpeedKmh {
		return httpx.BadRequest("Velocidad inválida")
	}
	if s.Tracker != nil {
		if current, ok := s.Hub.Shift(c.ShiftID); ok {
			if last, ok := s.Tracker.LastFix(current.BusNumber); ok && time.Since(last) < trackerPriority {
				return nil
			}
		}
	}
	now := time.Now()
	shift, err := s.Hub.UpdatePosition(c.ShiftID, p.Lat, p.Lon, p.SpeedKmh, now)
	var tooFrequent live.TooFrequentError
	switch {
	case errors.As(err, &tooFrequent):
		return httpx.ErrTooMany
	case err != nil:
		return httpx.NewError(http.StatusUnauthorized, "shift_closed", "El turno ya terminó")
	}
	s.Recorder.Record(shift, p.Lat, p.Lon, p.SpeedKmh, now)
	return nil
}

// OsmAnd is GET/POST /api/v1/driver/osmand: the Traccar OsmAnd protocol the app's DriverViewModel
// already speaks (id, lat, lon, speed in knots, optional alarm/status), plus the shift's Bearer token.
// `id` must match the bus of the token's shift, so a driver can't move another bus.
func (s *Service) OsmAnd(w http.ResponseWriter, r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return httpx.BadRequest("Parámetros inválidos")
	}
	c := auth.FromContext(r.Context())
	shift, ok := s.Hub.Shift(c.ShiftID)
	if !ok {
		return httpx.ErrUnauthorized
	}
	if !strings.EqualFold(r.Form.Get("id"), shift.BusNumber) {
		return httpx.ErrForbidden
	}

	switch alarm := r.Form.Get("alarm"); alarm {
	case "":
	case "accident", "sos":
		return s.raiseIncident(w, r, c, "accident")
	case "fault":
		return s.raiseIncident(w, r, c, "breakdown")
	default:
		return httpx.BadRequest("alarm desconocida")
	}
	if status := r.Form.Get("status"); status != "" {
		if !finalLocations[status] {
			return httpx.BadRequest("status desconocido")
		}
		if err := s.closeShift(r.Context(), c.ShiftID, "completed", status); err != nil {
			return err
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}

	var p positionReq
	var knots float64
	if _, err := fmt.Sscan(r.Form.Get("lat"), &p.Lat); err != nil {
		return httpx.BadRequest("lat inválida")
	}
	if _, err := fmt.Sscan(r.Form.Get("lon"), &p.Lon); err != nil {
		return httpx.BadRequest("lon inválida")
	}
	if v := r.Form.Get("speed"); v != "" {
		if _, err := fmt.Sscan(v, &knots); err != nil {
			return httpx.BadRequest("speed inválida")
		}
	}
	p.SpeedKmh = knots * 1.852
	if err := s.acceptPosition(c, p); err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

// TrackerStatus is GET /api/v1/driver/tracker: lets the driver's console show whether the bus
// position is arriving (busFixAgeSeconds) and whether Traccar Client delivers it (lastFixAgeSeconds).
func (s *Service) TrackerStatus(w http.ResponseWriter, r *http.Request) error {
	c := auth.FromContext(r.Context())
	shift, ok := s.Hub.Shift(c.ShiftID)
	if !ok {
		return httpx.ErrUnauthorized
	}
	out := map[string]any{"positionSource": "app"}
	// Whatever source delivered it: with the app's GPS as fallback the bus is on the map even when
	// Traccar Client isn't set up, and the driver's screen must not say otherwise.
	if at, ok := s.Hub.LastFixTime(shift.ID); ok {
		out["busFixAgeSeconds"] = int(time.Since(at).Seconds())
	}
	if s.Tracker != nil {
		out["positionSource"] = "traccar"
		out["trackerDeviceId"] = shift.BusNumber
		out["trackerUrl"] = s.Tracker.PublicURL()
		out["trackerConnected"] = s.Tracker.Connected()
		if last, ok := s.Tracker.LastFix(shift.BusNumber); ok {
			out["lastFixAt"] = last.UTC()
			out["lastFixAgeSeconds"] = int(time.Since(last).Seconds())
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// ---- incidents ----

// ReportIncident is POST /api/v1/driver/incidents {"type": "accident"|"breakdown"|"ramp_failure"}: alerts
// riders on the route immediately. Accidents and breakdowns close the shift, as the app's panic slider
// does; with a broken ramp the bus keeps its route, marked without ramp until the workshop fixes it.
func (s *Service) ReportIncident(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Type string `json:"type"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if req.Type != "accident" && req.Type != "breakdown" && req.Type != "ramp_failure" {
		return httpx.BadRequest("type debe ser accident, breakdown o ramp_failure")
	}
	return s.raiseIncident(w, r, auth.FromContext(r.Context()), req.Type)
}

func (s *Service) raiseIncident(w http.ResponseWriter, r *http.Request, c *auth.Claims, kind string) error {
	id, err := s.Report(r.Context(), c.ShiftID, kind)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"incidentId": id})
	return nil
}

// Report records a driver's incident ("accident", "breakdown" or "ramp_failure") and alerts riders on
// the route. Accidents and breakdowns close the shift; with a broken ramp the bus keeps its route,
// marked without ramp until the workshop fixes it.
func (s *Service) Report(ctx context.Context, shiftID uint64, kind string) (uint64, error) {
	shift, ok := s.Hub.Shift(shiftID)
	if !ok {
		return 0, httpx.ErrUnauthorized
	}
	var lat, lon any
	if la, lo, ok := s.Hub.LastFix(shift.ID); ok {
		lat, lon = la, lo
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO incidents (shift_id, bus_id, route_id, type, lat, lon) VALUES (?, ?, ?, ?, ?, ?)`,
		shift.ID, shift.BusID, shift.RouteID, kind, lat, lon)
	if err != nil {
		return 0, err
	}
	n, _ := res.LastInsertId()
	id := uint64(n)
	if err := s.noteOnVehicleReport(ctx, shift, kind, time.Now()); err != nil {
		// The incident is what matters; the form can still be written from the panel.
		slog.Error("anotando la falla en el reporte de vehículo", "incidente", id, "error", err)
	}
	if kind == "ramp_failure" {
		// Same as the panel marking the ramp broken: riders' ETAs skip this bus at once.
		if _, err := s.DB.ExecContext(ctx, `UPDATE buses SET status = 'ramp_broken', ramp_operational = FALSE
			WHERE id = ? AND status = 'active'`, shift.BusID); err != nil {
			return 0, err
		}
		s.Hub.SetRamp(shift.BusID, false)
		s.Hub.Alarm(id, shift.BusID, "rampFault")
		slog.Warn("falla de rampa reportada", "id", id, "unidad", shift.BusNumber, "ruta", shift.RouteID)
		return id, nil
	}
	alarm := "accident"
	if kind == "breakdown" {
		alarm = "fault"
	}
	// Alarm first: riders still know the bus's route when the event arrives.
	s.Hub.Alarm(id, shift.BusID, alarm)
	if err := s.closeShift(ctx, shift.ID, "emergency", ""); err != nil {
		return 0, err
	}
	slog.Warn("incidente reportado", "id", id, "tipo", kind, "unidad", shift.BusNumber, "ruta", shift.RouteID)
	return id, nil
}

// What the driver reported, as it reads on the vehicle report.
var incidentText = map[string]string{"accident": "Accidente", "breakdown": "Falla del camión", "ramp_failure": "Falla de rampa"}

// maxObservations is vehicle_reports.observations' size.
const maxObservations = 2000

// noteOnVehicleReport writes the driver's report on the bus's vehicle report of the day ("Reporte de
// vehículo"), so the form fills itself: the system keeps one report per bus and day (created_by NULL)
// and adds a line per report; the ones the panel opens by hand are left alone.
func (s *Service) noteOnVehicleReport(ctx context.Context, shift live.Shift, kind string, at time.Time) error {
	loc := s.Location
	if loc == nil {
		loc = time.Local
	}
	at = at.In(loc)
	var driver string
	if err := s.DB.QueryRowContext(ctx, `SELECT name FROM drivers WHERE id = ?`, shift.DriverID).Scan(&driver); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		return err
	}
	route := shift.RouteID
	if r, ok := s.Catalog.Route(shift.RouteID); ok {
		route = r.Name
	}
	line := fmt.Sprintf("%s %s reportada por el chofer %s en %s.", at.Format("15:04"), incidentText[kind], strings.TrimSpace(driver), route)
	if driver == "" {
		line = fmt.Sprintf("%s %s reportada por el chofer en %s.", at.Format("15:04"), incidentText[kind], route)
	}
	line = strings.ToUpper(line)

	day := at.Format(time.DateOnly)
	var (
		id  uint64
		obs string
	)
	err := s.DB.QueryRowContext(ctx, `SELECT id, observations FROM vehicle_reports
		WHERE bus_id = ? AND day = ? AND created_by IS NULL ORDER BY id LIMIT 1`, shift.BusID, day).Scan(&id, &obs)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = s.DB.ExecContext(ctx, `INSERT INTO vehicle_reports (bus_id, day, observations) VALUES (?, ?, ?)`,
			shift.BusID, day, line)
		return err
	case err != nil:
		return err
	}
	if obs != "" {
		line = obs + "\n" + line
	}
	if r := []rune(line); len(r) > maxObservations {
		line = string(r[:maxObservations])
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE vehicle_reports SET observations = ? WHERE id = ?`, line, id)
	return err
}

// ---- ramp requests ----

// RequestRamp is POST /api/v1/ramp-requests {"routeId", "stopId"} from a rider. Anonymous; repeated
// requests for the same stop within 5 minutes return the existing one instead of spamming drivers.
func (s *Service) RequestRamp(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		RouteID string `json:"routeId"`
		StopID  string `json:"stopId"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	stop, ok := s.Catalog.Stop(req.StopID)
	if !ok || stop.RouteID != req.RouteID {
		return httpx.BadRequest("La parada no pertenece a la ruta")
	}
	if !stop.Accessible {
		return httpx.BadRequest("La parada no es accesible")
	}

	ctx := r.Context()
	var id uint64
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT id, status FROM ramp_requests
		WHERE stop_id = ? AND status = 'pending' AND created_at > NOW(3) - INTERVAL 5 MINUTE ORDER BY id DESC LIMIT 1`,
		stop.ID).Scan(&id, &status)
	if err == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "status": status, "duplicate": true})
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	res, err := s.DB.ExecContext(ctx, `INSERT INTO ramp_requests (route_id, stop_id) VALUES (?, ?)`, stop.RouteID, stop.ID)
	if err != nil {
		return err
	}
	newID, _ := res.LastInsertId()
	ramp := live.RampRequest{
		ID: uint64(newID), RouteID: stop.RouteID, StopID: stop.ID, StopName: stop.Name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	notified := s.Hub.NotifyRamp(ramp)
	if s.OnRamp != nil {
		notified += s.OnRamp(ramp)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": newID, "status": "pending", "driversNotified": notified})
	return nil
}

// RampStatus is GET /api/v1/ramp-requests/{id}, so the rider's app can show "the driver saw it".
func (s *Service) RampStatus(w http.ResponseWriter, r *http.Request) error {
	var status string
	err := s.DB.QueryRowContext(r.Context(), `SELECT status FROM ramp_requests WHERE id = ?`, r.PathValue("id")).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": status})
	return nil
}

// AckRamp is POST /api/v1/driver/ramp-requests/{id}/ack: only a driver on that route can ack it.
func (s *Service) AckRamp(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := s.Acknowledge(r.Context(), auth.FromContext(r.Context()).ShiftID, id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Acknowledge marks a pending ramp request as seen by the driver of shiftID (who must be on its route).
func (s *Service) Acknowledge(ctx context.Context, shiftID, requestID uint64) error {
	shift, ok := s.Hub.Shift(shiftID)
	if !ok {
		return httpx.ErrUnauthorized
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE ramp_requests SET status = 'acknowledged', acknowledged_at = CURRENT_TIMESTAMP(3),
		ack_shift_id = ? WHERE id = ? AND route_id = ? AND status = 'pending'`, shift.ID, requestID, shift.RouteID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.NewError(http.StatusConflict, "not_pending", "La solicitud ya no está pendiente")
	}
	s.Hub.NotifyRampAck(shift.RouteID, requestID)
	return nil
}

// PendingRamps feeds the driver socket on connect.
func (s *Service) PendingRamps(ctx context.Context, routeID string) []live.RampRequest {
	rows, err := s.DB.QueryContext(ctx, `SELECT r.id, r.route_id, r.stop_id, st.name, r.created_at
		FROM ramp_requests r JOIN stops st ON st.id = r.stop_id
		WHERE r.route_id = ? AND r.status = 'pending' AND r.created_at > NOW(3) - INTERVAL 15 MINUTE
		ORDER BY r.id LIMIT 20`, routeID)
	if err != nil {
		slog.Error("solicitudes pendientes", "error", err)
		return nil
	}
	defer rows.Close()
	var out []live.RampRequest
	for rows.Next() {
		var q live.RampRequest
		var at time.Time
		if err := rows.Scan(&q.ID, &q.RouteID, &q.StopID, &q.StopName, &at); err != nil {
			return out
		}
		q.CreatedAt = at.UTC().Format(time.RFC3339)
		out = append(out, q)
	}
	return out
}

// ---- rider reports ----

var reportTypes = map[string]bool{"ramp_broken": true, "did_not_stop": true, "bus_full": true, "stop_blocked": true}

// CreateReport is POST /api/v1/reports, the app's Reportes screen.
func (s *Service) CreateReport(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Type    string  `json:"type"`
		RouteID *string `json:"routeId"`
		BusID   *uint64 `json:"busId"`
		Comment string  `json:"comment"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	if !reportTypes[req.Type] {
		return httpx.BadRequest("type debe ser ramp_broken, did_not_stop, bus_full o stop_blocked")
	}
	if req.RouteID != nil {
		if _, ok := s.Catalog.Route(*req.RouteID); !ok {
			return httpx.BadRequest("Ruta desconocida")
		}
	}
	req.Comment = strings.TrimSpace(req.Comment)
	if len([]rune(req.Comment)) > 500 {
		return httpx.BadRequest("El comentario no puede pasar de 500 caracteres")
	}
	res, err := s.DB.ExecContext(r.Context(), `INSERT INTO reports (type, route_id, bus_id, comment) VALUES (?, ?, ?, ?)`,
		req.Type, req.RouteID, req.BusID, req.Comment)
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"id": id})
	return nil
}

// ---- housekeeping ----

// RunJanitor expires stale ramp requests and shifts, and trims the position history.
func (s *Service) RunJanitor(ctx context.Context, retention time.Duration) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	lastPurge := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.DB.ExecContext(ctx, `UPDATE ramp_requests SET status = 'expired'
			WHERE status = 'pending' AND created_at < NOW(3) - INTERVAL ? SECOND`, int(rampRequestTTL.Seconds())); err != nil {
			slog.Error("expirando solicitudes de rampa", "error", err)
		}
		s.expireShifts(ctx)
		if time.Since(lastPurge) > time.Hour {
			lastPurge = time.Now()
			s.purgePositions(ctx, retention)
		}
	}
}

// expireShifts closes shifts older than the token lifetime: their driver can no longer report anyway.
func (s *Service) expireShifts(ctx context.Context) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM shifts WHERE ended_at IS NULL AND started_at < ?`,
		time.Now().Add(-s.TokenTTL).UTC())
	if err != nil {
		slog.Error("buscando turnos vencidos", "error", err)
		return
	}
	var ids []uint64
	for rows.Next() {
		var id uint64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := s.closeShift(ctx, id, "expired", ""); err != nil {
			slog.Error("cerrando turno vencido", "turno", id, "error", err)
		}
	}
}

// purgePositions deletes in chunks so a big purge never holds long locks.
func (s *Service) purgePositions(ctx context.Context, retention time.Duration) {
	cutoff := time.Now().Add(-retention).UTC()
	total := int64(0)
	for {
		res, err := s.DB.ExecContext(ctx, `DELETE FROM positions WHERE recorded_at < ? LIMIT 5000`, cutoff)
		if err != nil {
			slog.Error("purgando posiciones", "error", err)
			return
		}
		n, _ := res.RowsAffected()
		total += n
		if n < 5000 {
			break
		}
	}
	if total > 0 {
		slog.Info("posiciones antiguas purgadas", "n", total)
	}
}

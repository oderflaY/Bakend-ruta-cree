// Package admin is the API behind an operations dashboard: fleet overview, drivers, buses, routes,
// rider reports, incidents and ramp requests. Every route except Login needs an admin token.
package admin

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/dispatch"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/live"
	"github.com/eter/rutacree-backend/internal/riders"
	"github.com/eter/rutacree-backend/internal/transit"
	"github.com/go-sql-driver/mysql"
)

type Handler struct {
	DB       *sql.DB
	Hub      *live.Hub
	Catalog  *transit.Catalog
	Tokens   *auth.Tokens
	Dispatch *dispatch.Service
	TokenTTL time.Duration
	// Local time zone of the printed forms (dates and trip times).
	Location *time.Location
	// Street routing for the route editor (OSRM); nil turns the snapping off.
	Routing *Router
}

// EnsureAdmin creates the first admin from ADMIN_EMAIL / ADMIN_PASSWORD when there is none.
func EnsureAdmin(ctx context.Context, db *sql.DB, email, password string) (bool, error) {
	if email == "" {
		return false, nil
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return false, err
	}
	username := strings.SplitN(email, "@", 2)[0]
	if len(username) > 40 {
		username = username[:40]
	}
	_, err = db.ExecContext(ctx, `INSERT INTO admins (name, username, email, role, password_hash) VALUES (?, ?, ?, 'superadmin', ?)`,
		username, username, email, hash)
	return err == nil, err
}

// Login is POST /api/v1/admin/login {"email", "password"}.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || len(email) > 190 || len(req.Password) > 256 {
		auth.BurnTime(req.Password)
		return httpx.ErrUnauthorized
	}
	ctx := r.Context()
	var (
		id          uint64
		hash        string
		lockedUntil sql.NullTime
		active      bool
	)
	err := h.DB.QueryRowContext(ctx, `SELECT id, password_hash, locked_until, active FROM admins WHERE email = ?`, email).
		Scan(&id, &hash, &lockedUntil, &active)
	if errors.Is(err, sql.ErrNoRows) {
		auth.BurnTime(req.Password)
		return httpx.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if auth.Locked(lockedUntil) {
		return httpx.NewError(http.StatusTooManyRequests, "locked", "Demasiados intentos fallidos, espera 15 minutos")
	}
	ok, err := auth.VerifyPassword(req.Password, hash)
	if err != nil {
		return err
	}
	if !ok {
		if err := auth.AdminAccounts.RecordFailure(ctx, h.DB, id); err != nil {
			return err
		}
		return httpx.ErrUnauthorized
	}
	// Checked after the password so a wrong guess can't tell a deactivated account from a missing one.
	if !active {
		return httpx.NewError(http.StatusForbidden, "inactive", "Esta cuenta fue dada de baja")
	}
	if err := auth.AdminAccounts.RecordSuccess(ctx, h.DB, id); err != nil {
		return err
	}
	token, exp, err := h.Tokens.Issue(auth.Claims{Role: auth.RoleAdmin, RegisteredClaims: auth.Subject(strconv.FormatUint(id, 10))}, h.TokenTTL)
	if err != nil {
		return err
	}
	u, err := h.loadUser(ctx, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": token, "expiresAt": exp, "account": u})
	return nil
}

// Overview is GET /api/v1/admin/overview: the numbers for the dashboard's header cards.
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	active := h.Hub.ActiveShifts()
	reporting := 0
	for _, s := range active {
		if s.LastFix != nil && time.Since(*s.LastFix) < 2*time.Minute {
			reporting++
		}
	}
	out := map[string]any{"activeShifts": len(active), "busesReporting": reporting}

	counts := []struct {
		key, q string
	}{
		{"pendingRampRequests", `SELECT COUNT(*) FROM ramp_requests WHERE status = 'pending'`},
		{"openIncidents", `SELECT COUNT(*) FROM incidents WHERE resolved_at IS NULL`},
		{"openReports", `SELECT COUNT(*) FROM reports WHERE status = 'open'`},
		{"rampRequestsToday", `SELECT COUNT(*) FROM ramp_requests WHERE created_at >= UTC_DATE()`},
	}
	for _, c := range counts {
		var n int
		if err := h.DB.QueryRowContext(ctx, c.q).Scan(&n); err != nil {
			return err
		}
		out[c.key] = n
	}

	rows, err := h.DB.QueryContext(ctx, `SELECT type, COUNT(*) FROM reports WHERE created_at > NOW(3) - INTERVAL 1 DAY GROUP BY type`)
	if err != nil {
		return err
	}
	defer rows.Close()
	byType := map[string]int{}
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return err
		}
		byType[t] = n
	}
	out["reportsLast24hByType"] = byType
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// ActiveShifts is GET /api/v1/admin/shifts/active.
func (h *Handler) ActiveShifts(w http.ResponseWriter, r *http.Request) error {
	httpx.WriteJSON(w, http.StatusOK, h.Hub.ActiveShifts())
	return nil
}

type trip struct {
	ShiftID       uint64     `json:"shiftId"`
	DriverID      string     `json:"driverId"`
	DriverName    string     `json:"driverName"`
	BusNumber     string     `json:"busNumber"`
	RouteID       string     `json:"routeId"`
	StartedAt     time.Time  `json:"startedAt"`
	EndedAt       *time.Time `json:"endedAt"`
	EndReason     *string    `json:"endReason"`
	FinalLocation *string    `json:"finalLocation"`
	Disability    *int       `json:"disability"`
	Elderly       *int       `json:"elderly"`
	Pregnant      *int       `json:"pregnant"`
	Companions    *int       `json:"companions"`
	Men           *int       `json:"men"`
	Women         *int       `json:"women"`
	// Trips logged by older apps (before the DIF's categories).
	Children *int `json:"children"`
	Adults   *int `json:"adults"`
}

// ListShifts is GET /api/v1/admin/shifts?from=RFC3339&to=RFC3339: the routes driven in that window
// (by start time), with when each began and ended and the passengers the driver counted at the start.
func (h *Handler) ListShifts(w http.ResponseWriter, r *http.Request) error {
	from, err := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	if err != nil {
		return httpx.BadRequest("from debe ser una fecha RFC3339")
	}
	to, err := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err != nil || !to.After(from) {
		return httpx.BadRequest("to debe ser una fecha RFC3339 posterior a from")
	}
	rows, err := h.DB.QueryContext(r.Context(), `SELECT s.id, s.driver_id, d.name, b.number, s.route_id, s.started_at,
		s.ended_at, s.end_reason, s.final_location, s.disability, s.elderly, s.pregnant, s.companions, s.men,
		s.women, s.children, s.adults
		FROM shifts s JOIN drivers d ON d.id = s.driver_id JOIN buses b ON b.id = s.bus_id
		WHERE s.started_at >= ? AND s.started_at < ? ORDER BY s.started_at DESC LIMIT ?`,
		from.UTC(), to.UTC(), limitParam(r))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []trip{}
	for rows.Next() {
		var t trip
		if err := rows.Scan(&t.ShiftID, &t.DriverID, &t.DriverName, &t.BusNumber, &t.RouteID, &t.StartedAt,
			&t.EndedAt, &t.EndReason, &t.FinalLocation, &t.Disability, &t.Elderly, &t.Pregnant, &t.Companions, &t.Men,
			&t.Women, &t.Children, &t.Adults); err != nil {
			return err
		}
		out = append(out, t)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// ---- drivers ----

type driver struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	Active    bool      `json:"active"`
	CanLogin  bool      `json:"canLogin"`
	Locked    bool      `json:"locked"`
	CreatedAt time.Time `json:"createdAt"`
}

const driverColumns = `id, name, COALESCE(email, ''), phone, active, password_hash IS NOT NULL, locked_until, created_at`

func scanDriver(sc interface{ Scan(...any) error }) (driver, error) {
	var d driver
	var locked sql.NullTime
	err := sc.Scan(&d.ID, &d.Name, &d.Email, &d.Phone, &d.Active, &d.CanLogin, &locked, &d.CreatedAt)
	d.Locked = auth.Locked(locked)
	return d, err
}

func (h *Handler) loadDriver(ctx context.Context, id string) (driver, error) {
	d, err := scanDriver(h.DB.QueryRowContext(ctx, `SELECT `+driverColumns+` FROM drivers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, httpx.ErrNotFound
	}
	return d, err
}

func (h *Handler) ListDrivers(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT `+driverColumns+` FROM drivers ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []driver{}
	for rows.Next() {
		d, err := scanDriver(rows)
		if err != nil {
			return err
		}
		out = append(out, d)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// driverEmail validates a driver's login email and checks no rider account uses it.
func (h *Handler) driverEmail(r *http.Request, raw string) (string, error) {
	email, err := riders.NormalizeEmail(raw)
	if err != nil {
		return "", err
	}
	var taken int
	if err := h.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM riders WHERE email = ?`, email).Scan(&taken); err != nil {
		return "", err
	}
	if taken > 0 {
		return "", httpx.NewError(http.StatusConflict, "email_taken", "Ese correo ya pertenece a una cuenta de pasajero")
	}
	return email, nil
}

var phonePattern = regexp.MustCompile(`^\+?[0-9 ]{0,19}$`)

func cleanPhone(p string) (string, error) {
	p = strings.TrimSpace(p)
	if !phonePattern.MatchString(p) {
		return "", httpx.BadRequest("Teléfono inválido (solo números, espacios y +)")
	}
	return p, nil
}

func validPassword(p string) error {
	if n := utf8.RuneCountInString(p); n < riders.MinPassword || n > riders.MaxPassword {
		return httpx.NewError(http.StatusBadRequest, "weak_password", "La contraseña debe tener entre 8 y 128 caracteres")
	}
	return nil
}

// CreateDriver is POST /api/v1/admin/drivers {"id", "name", "email", "phone"?, "password"}.
func (h *Handler) CreateDriver(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		Phone    string `json:"phone"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	req.ID = strings.ToUpper(strings.TrimSpace(req.ID))
	if !dispatch.DriverIDPattern.MatchString(req.ID) {
		return httpx.BadRequest("id inválido (A-Z, 0-9 y guiones, máx. 16)")
	}
	name, err := cleanName(req.Name)
	if err != nil {
		return err
	}
	phone, err := cleanPhone(req.Phone)
	if err != nil {
		return err
	}
	email, err := h.driverEmail(r, req.Email)
	if err != nil {
		return err
	}
	if err := validPassword(req.Password); err != nil {
		return err
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return err
	}
	if _, err := h.DB.ExecContext(r.Context(), `INSERT INTO drivers (id, name, email, phone, password_hash) VALUES (?, ?, ?, ?, ?)`,
		req.ID, name, email, phone, hash); err != nil {
		if me, ok := errors.AsType[*mysql.MySQLError](err); ok && me.Number == 1062 {
			return httpx.NewError(http.StatusConflict, "exists", "Ya existe un chofer con ese id o correo")
		}
		return err
	}
	h.audit(r, "dio de alta al chofer %s (%s)", name, req.ID)
	d, err := h.loadDriver(r.Context(), req.ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, d)
	return nil
}

// UpdateDriver is PATCH /api/v1/admin/drivers/{id} {"name"?, "active"?, "email"?, "phone"?, "password"?}.
// A new password also unlocks the account; deactivating closes the driver's open shift.
func (h *Handler) UpdateDriver(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Name     *string `json:"name"`
		Active   *bool   `json:"active"`
		Email    *string `json:"email"`
		Phone    *string `json:"phone"`
		Password *string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	id := r.PathValue("id")
	before, err := h.loadDriver(r.Context(), id)
	if err != nil {
		return err
	}
	sets, args, changes := []string{}, []any{}, []string{}
	if req.Name != nil {
		n, err := cleanName(*req.Name)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "name = ?"), append(args, n), append(changes, "nombre")
	}
	if req.Active != nil && *req.Active != before.Active {
		sets, args = append(sets, "active = ?"), append(args, *req.Active)
		changes = append(changes, map[bool]string{true: "reactivado", false: "dado de baja"}[*req.Active])
	}
	if req.Email != nil {
		email, err := h.driverEmail(r, *req.Email)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "email = ?"), append(args, email), append(changes, "correo")
	}
	if req.Phone != nil {
		phone, err := cleanPhone(*req.Phone)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "phone = ?"), append(args, phone), append(changes, "teléfono")
	}
	if req.Password != nil {
		if err := validPassword(*req.Password); err != nil {
			return err
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			return err
		}
		sets, args = append(sets, "password_hash = ?", "failed_logins = 0", "locked_until = NULL"), append(args, hash)
		changes = append(changes, "contraseña")
	}
	if len(sets) == 0 {
		httpx.WriteJSON(w, http.StatusOK, before)
		return nil
	}
	_, err = h.DB.ExecContext(r.Context(), `UPDATE drivers SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...)
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok && me.Number == 1062 {
		return httpx.NewError(http.StatusConflict, "email_taken", "Otro chofer ya usa ese correo")
	}
	if err != nil {
		return err
	}
	if req.Active != nil && !*req.Active {
		if err := h.Dispatch.CloseDriverShifts(r.Context(), id); err != nil {
			return err
		}
	}
	h.audit(r, "editó al chofer %s (%s): %s", before.Name, id, strings.Join(changes, ", "))
	d, err := h.loadDriver(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

// ---- buses ----

// BusStatus: "workshop" takes the bus out of service (its open shift is closed); "ramp_broken"
// keeps it running but riders no longer count on it for a ramp; "retired" = dada de baja del parque.
type BusStatus string

var busStatusName = map[BusStatus]string{"active": "Activo", "workshop": "En Taller", "ramp_broken": "Rampa Dañada", "retired": "Baja"}

type bus struct {
	ID              uint64    `json:"id"`
	Number          string    `json:"number"`
	Plates          string    `json:"plates"`
	Brand           string    `json:"brand"`
	Model           string    `json:"model"`
	Serial          string    `json:"serial"`
	Year            *int      `json:"year"`
	UnitNo          string    `json:"unitNo"`
	Centro          string    `json:"centro"`
	Status          BusStatus `json:"status"`
	LastMaintenance *string   `json:"lastMaintenance"`
	Ramp            bool      `json:"rampOperational"`
	CreatedAt       time.Time `json:"createdAt"`
}

const busColumns = `id, number, plates, brand, model, serial, year, unit_no, centro, status, DATE_FORMAT(last_maintenance, '%Y-%m-%d'), ramp_operational, created_at`

func scanBus(sc interface{ Scan(...any) error }) (bus, error) {
	var b bus
	err := sc.Scan(&b.ID, &b.Number, &b.Plates, &b.Brand, &b.Model, &b.Serial, &b.Year, &b.UnitNo, &b.Centro, &b.Status, &b.LastMaintenance, &b.Ramp, &b.CreatedAt)
	return b, err
}

func (h *Handler) loadBus(ctx context.Context, id uint64) (bus, error) {
	b, err := scanBus(h.DB.QueryRowContext(ctx, `SELECT `+busColumns+` FROM buses WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return b, httpx.ErrNotFound
	}
	return b, err
}

func (h *Handler) ListBuses(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.DB.QueryContext(r.Context(), `SELECT `+busColumns+` FROM buses ORDER BY status = 'retired', centro <> 'CREE DURANGO', centro, LPAD(number, 16, '0')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []bus{}
	for rows.Next() {
		b, err := scanBus(rows)
		if err != nil {
			return err
		}
		out = append(out, b)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

var (
	platesPattern = regexp.MustCompile(`^[A-Z0-9-]{0,16}$`)
	serialPattern = regexp.MustCompile(`^[A-Z0-9]{0,20}$`)
)

// cleanLabel normalizes a short free-text bus field (brand, model) to upper case, as on the forms.
func cleanLabel(s, field string) (string, error) {
	s = strings.ToUpper(strings.Join(strings.Fields(s), " "))
	if utf8.RuneCountInString(s) > 40 {
		return "", httpx.BadRequest(field + " demasiado largo (máx. 40)")
	}
	return s, nil
}

func cleanPlates(p string) (string, error) {
	p = strings.ToUpper(strings.TrimSpace(p))
	if !platesPattern.MatchString(p) {
		return "", httpx.BadRequest("Placas inválidas (letras, números y guiones, máx. 16)")
	}
	return p, nil
}

// CreateBus is POST /api/v1/admin/buses {"number", "plates"?, "brand"?, "model"?}. Buses are also created on the fly when
// a driver opens a shift on a new number; this lets the fleet be registered beforehand.
func (h *Handler) CreateBus(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Number string `json:"number"`
		Plates string `json:"plates"`
		Brand  string `json:"brand"`
		Model  string `json:"model"`
		Serial string `json:"serial"`
		Year   *int   `json:"year"`
		UnitNo string `json:"unitNo"`
		Centro string `json:"centro"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	number := strings.ToUpper(strings.TrimSpace(req.Number))
	if !dispatch.BusNumberPattern.MatchString(number) {
		return httpx.BadRequest("Número de unidad inválido (hasta 16 letras, números o guiones)")
	}
	plates, err := cleanPlates(req.Plates)
	if err != nil {
		return err
	}
	brand, err := cleanLabel(req.Brand, "La marca")
	if err != nil {
		return err
	}
	model, err := cleanLabel(req.Model, "El modelo")
	if err != nil {
		return err
	}
	ext, err := cleanFleetFields(req.Serial, req.Year, req.UnitNo, req.Centro)
	if err != nil {
		return err
	}
	res, err := h.DB.ExecContext(r.Context(), `INSERT INTO buses (number, plates, brand, model, serial, year, unit_no, centro)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, number, plates, brand, model, ext.Serial, ext.Year, ext.UnitNo, ext.Centro)
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok && me.Number == 1062 {
		return httpx.NewError(http.StatusConflict, "exists", "Ya existe esa unidad")
	}
	if err != nil {
		return err
	}
	id, _ := res.LastInsertId()
	h.Dispatch.RegisterDevice(r.Context(), number)
	h.audit(r, "dio de alta la Unidad %s", number)
	b, err := h.loadBus(r.Context(), uint64(id))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, b)
	return nil
}

// UpdateBus is PATCH /api/v1/admin/buses/{id} {"status"?, "plates"?, "brand"?, "model"?, "lastMaintenance"?: "YYYY-MM-DD"|"",
// "rampOperational"?}. Ramp changes reach riders within a second; "workshop" closes the bus's shift.
func (h *Handler) UpdateBus(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Status          *BusStatus `json:"status"`
		Plates          *string    `json:"plates"`
		Brand           *string    `json:"brand"`
		Model           *string    `json:"model"`
		Serial          *string    `json:"serial"`
		Year            *int       `json:"year"`
		UnitNo          *string    `json:"unitNo"`
		Centro          *string    `json:"centro"`
		LastMaintenance *string    `json:"lastMaintenance"`
		Ramp            *bool      `json:"rampOperational"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	ctx := r.Context()
	before, err := h.loadBus(ctx, id)
	if err != nil {
		return err
	}
	// rampOperational alone (older clients) maps onto the status.
	if req.Status == nil && req.Ramp != nil {
		s := BusStatus("active")
		if !*req.Ramp {
			s = "ramp_broken"
		}
		req.Status = &s
	}

	sets, args, changes := []string{}, []any{}, []string{}
	if req.Status != nil && *req.Status != before.Status {
		if _, ok := busStatusName[*req.Status]; !ok {
			return httpx.BadRequest("status debe ser active, workshop, ramp_broken o retired")
		}
		sets = append(sets, "status = ?", "ramp_operational = ?")
		args = append(args, *req.Status, *req.Status != "ramp_broken")
		changes = append(changes, "estado a "+busStatusName[*req.Status])
	}
	if req.Plates != nil {
		p, err := cleanPlates(*req.Plates)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "plates = ?"), append(args, p), append(changes, "placas")
	}
	if req.Brand != nil {
		v, err := cleanLabel(*req.Brand, "La marca")
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "brand = ?"), append(args, v), append(changes, "marca")
	}
	if req.Model != nil {
		v, err := cleanLabel(*req.Model, "El modelo")
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "model = ?"), append(args, v), append(changes, "modelo")
	}
	if req.Serial != nil || req.Year != nil || req.UnitNo != nil || req.Centro != nil {
		pick := func(p *string, v string) string {
			if p != nil {
				return *p
			}
			return v
		}
		year := before.Year
		if req.Year != nil {
			year = req.Year
			if *year == 0 {
				year = nil
			}
		}
		ext, err := cleanFleetFields(pick(req.Serial, before.Serial), year, pick(req.UnitNo, before.UnitNo), pick(req.Centro, before.Centro))
		if err != nil {
			return err
		}
		sets = append(sets, "serial = ?", "year = ?", "unit_no = ?", "centro = ?")
		args = append(args, ext.Serial, ext.Year, ext.UnitNo, ext.Centro)
		changes = append(changes, "datos del parque vehicular")
	}
	if req.LastMaintenance != nil {
		var day any
		if v := strings.TrimSpace(*req.LastMaintenance); v != "" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return httpx.BadRequest("lastMaintenance debe ser YYYY-MM-DD")
			}
			day = v
		}
		sets, args, changes = append(sets, "last_maintenance = ?"), append(args, day), append(changes, "último mantenimiento")
	}
	if len(sets) == 0 {
		httpx.WriteJSON(w, http.StatusOK, before)
		return nil
	}
	if _, err := h.DB.ExecContext(ctx, `UPDATE buses SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		return err
	}
	if req.Status != nil && *req.Status != before.Status {
		h.Hub.SetRamp(id, *req.Status != "ramp_broken")
		if *req.Status == "workshop" || *req.Status == "retired" {
			if err := h.Dispatch.CloseBusShifts(ctx, id); err != nil {
				return err
			}
		}
	}
	h.audit(r, "editó la Unidad %s: %s", before.Number, strings.Join(changes, ", "))
	b, err := h.loadBus(ctx, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, b)
	return nil
}

// cleanFleetFields validates the fleet-sheet fields of a bus (serie, año, No. de camión, centro).
func cleanFleetFields(serial string, year *int, unitNo, centro string) (FleetBus, error) {
	b := FleetBus{Serial: strings.ToUpper(strings.ReplaceAll(serial, " ", "")), Year: year,
		UnitNo: strings.ToUpper(strings.TrimSpace(unitNo)), Centro: fold(centro)}
	switch {
	case !serialPattern.MatchString(b.Serial):
		return b, httpx.BadRequest("Serie inválida (letras y números, máx. 20)")
	case year != nil && (*year < 1950 || *year > 2100):
		return b, httpx.BadRequest("Año inválido")
	case utf8.RuneCountInString(b.UnitNo) > 8:
		return b, httpx.BadRequest("No. de camión demasiado largo (máx. 8)")
	case utf8.RuneCountInString(b.Centro) > 60:
		return b, httpx.BadRequest("Centro demasiado largo (máx. 60)")
	}
	return b, nil
}

// Track is GET /api/v1/admin/buses/{id}/track?from=RFC3339&to=RFC3339 (max 24 h, 5000 points).
func (h *Handler) Track(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	to := time.Now()
	if v := r.URL.Query().Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			return httpx.BadRequest("to inválido (RFC3339)")
		}
	}
	from := to.Add(-time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			return httpx.BadRequest("from inválido (RFC3339)")
		}
	}
	if !from.Before(to) || to.Sub(from) > 24*time.Hour {
		return httpx.BadRequest("Rango inválido (máximo 24 horas)")
	}
	rows, err := h.DB.QueryContext(r.Context(), `SELECT lat, lon, speed_kmh, recorded_at FROM positions
		WHERE bus_id = ? AND recorded_at BETWEEN ? AND ? ORDER BY recorded_at LIMIT 5000`, id, from.UTC(), to.UTC())
	if err != nil {
		return err
	}
	defer rows.Close()
	type point struct {
		Lat   float64   `json:"lat"`
		Lon   float64   `json:"lon"`
		Speed float64   `json:"speedKmh"`
		At    time.Time `json:"at"`
	}
	out := []point{}
	for rows.Next() {
		var p point
		if err := rows.Scan(&p.Lat, &p.Lon, &p.Speed, &p.At); err != nil {
			return err
		}
		out = append(out, p)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// ---- routes ----

// PutRoute is PUT /api/v1/admin/routes: body is a route GeoJSON (same format as the app's files).
func (h *Handler) PutRoute(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "GeoJSON demasiado grande (máx. 4 MB)")
	}
	route, err := transit.ParseRoute(body)
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	if err := h.Catalog.Upsert(r.Context(), route); err != nil {
		return err
	}
	accessible := 0
	for _, s := range route.Stops {
		if s.Accessible {
			accessible++
		}
	}
	h.audit(r, "publicó la ruta %s (%d paradas, %d accesibles)", route.Name, len(route.Stops), accessible)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"id": route.ID, "stops": len(route.Stops)})
	return nil
}

func (h *Handler) DeleteRoute(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	if err := h.Catalog.Deactivate(r.Context(), id); err != nil {
		return err
	}
	h.audit(r, "retiró la ruta %s", id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- reports, incidents, ramp requests ----

func limitParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 || n > 500 {
		return 100
	}
	return n
}

func cleanResolution(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > 1000 {
		return "", httpx.BadRequest("La resolución no puede pasar de 1000 caracteres")
	}
	return s, nil
}

// report carries the cross-check with the GPS history: who drove the bus and how fast it went
// when the rider reported it. Reports are anonymous, so there is nobody to notify back.
type report struct {
	ID         uint64    `json:"id"`
	Type       string    `json:"type"`
	RouteID    *string   `json:"routeId"`
	BusID      *uint64   `json:"busId"`
	BusNumber  *string   `json:"busNumber"`
	Comment    string    `json:"comment"`
	Status     string    `json:"status"`
	Resolution string    `json:"resolution"`
	CreatedAt  time.Time `json:"createdAt"`
	DriverName *string   `json:"driverName"`
	SpeedKmh   *float64  `json:"speedKmh"`
}

const reportQuery = `SELECT r.id, r.type, r.route_id, r.bus_id, b.number, r.comment, r.status, r.resolution, r.created_at,
	(SELECT d.name FROM shifts s JOIN drivers d ON d.id = s.driver_id
		WHERE s.bus_id = r.bus_id AND s.started_at <= r.created_at AND (s.ended_at IS NULL OR s.ended_at >= r.created_at)
		ORDER BY s.started_at DESC LIMIT 1),
	(SELECT p.speed_kmh FROM positions p
		WHERE p.bus_id = r.bus_id AND p.recorded_at BETWEEN r.created_at - INTERVAL 2 MINUTE AND r.created_at + INTERVAL 2 MINUTE
		ORDER BY ABS(TIMESTAMPDIFF(MICROSECOND, p.recorded_at, r.created_at)) LIMIT 1)
	FROM reports r LEFT JOIN buses b ON b.id = r.bus_id`

func scanReport(sc interface{ Scan(...any) error }) (report, error) {
	var rp report
	err := sc.Scan(&rp.ID, &rp.Type, &rp.RouteID, &rp.BusID, &rp.BusNumber, &rp.Comment, &rp.Status, &rp.Resolution,
		&rp.CreatedAt, &rp.DriverName, &rp.SpeedKmh)
	return rp, err
}

var reportStatuses = map[string]bool{"open": true, "in_review": true, "reviewed": true, "dismissed": true}

// ListReports is GET /api/v1/admin/reports?status=open|in_review|reviewed|dismissed|all&limit=N.
func (h *Handler) ListReports(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	q, args := reportQuery, []any{}
	if status != "all" {
		if !reportStatuses[status] {
			return httpx.BadRequest("status inválido")
		}
		q, args = q+` WHERE r.status = ?`, append(args, status)
	}
	rows, err := h.DB.QueryContext(r.Context(), q+` ORDER BY r.id DESC LIMIT ?`, append(args, limitParam(r))...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []report{}
	for rows.Next() {
		rp, err := scanReport(rows)
		if err != nil {
			return err
		}
		out = append(out, rp)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

// UpdateReport is PATCH /api/v1/admin/reports/{id} {"status"?, "resolution"?} and answers the report.
func (h *Handler) UpdateReport(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Status     *string `json:"status"`
		Resolution *string `json:"resolution"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	sets, args, changes := []string{}, []any{}, []string{}
	if req.Status != nil {
		if !reportStatuses[*req.Status] {
			return httpx.BadRequest("status debe ser open, in_review, reviewed o dismissed")
		}
		sets, args, changes = append(sets, "status = ?"), append(args, *req.Status), append(changes, "estado a "+*req.Status)
	}
	if req.Resolution != nil {
		res, err := cleanResolution(*req.Resolution)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "resolution = ?"), append(args, res), append(changes, "resolución")
	}
	if len(sets) == 0 {
		return httpx.BadRequest("Nada que actualizar")
	}
	id := r.PathValue("id")
	if _, err := h.DB.ExecContext(r.Context(), `UPDATE reports SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		return err
	}
	rp, err := scanReport(h.DB.QueryRowContext(r.Context(), reportQuery+` WHERE r.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	h.audit(r, "actualizó el reporte QJA-%04d: %s", rp.ID, strings.Join(changes, ", "))
	httpx.WriteJSON(w, http.StatusOK, rp)
	return nil
}

type incident struct {
	ID         uint64     `json:"id"`
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	Resolution string     `json:"resolution"`
	RouteID    string     `json:"routeId"`
	BusID      uint64     `json:"busId"`
	BusNumber  string     `json:"busNumber"`
	DriverID   string     `json:"driverId"`
	DriverName string     `json:"driverName"`
	Lat        *float64   `json:"lat"`
	Lon        *float64   `json:"lon"`
	CreatedAt  time.Time  `json:"createdAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
}

const incidentQuery = `SELECT i.id, i.type, i.status, i.resolution, i.route_id, i.bus_id, b.number, s.driver_id, d.name,
	i.lat, i.lon, i.created_at, i.resolved_at
	FROM incidents i JOIN buses b ON b.id = i.bus_id JOIN shifts s ON s.id = i.shift_id JOIN drivers d ON d.id = s.driver_id`

func scanIncident(sc interface{ Scan(...any) error }) (incident, error) {
	var i incident
	err := sc.Scan(&i.ID, &i.Type, &i.Status, &i.Resolution, &i.RouteID, &i.BusID, &i.BusNumber, &i.DriverID, &i.DriverName,
		&i.Lat, &i.Lon, &i.CreatedAt, &i.ResolvedAt)
	return i, err
}

// ListIncidents is GET /api/v1/admin/incidents?open=true.
func (h *Handler) ListIncidents(w http.ResponseWriter, r *http.Request) error {
	q := incidentQuery
	if r.URL.Query().Get("open") == "true" {
		q += ` WHERE i.resolved_at IS NULL`
	}
	rows, err := h.DB.QueryContext(r.Context(), q+` ORDER BY i.id DESC LIMIT ?`, limitParam(r))
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return err
		}
		out = append(out, i)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

var incidentStatuses = map[string]bool{"open": true, "in_review": true, "resolved": true}

// UpdateIncident is PATCH /api/v1/admin/incidents/{id} {"status"?, "resolution"?}. resolved_at follows
// the status, so the overview's open count stays right.
func (h *Handler) UpdateIncident(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Status     *string `json:"status"`
		Resolution *string `json:"resolution"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	sets, args, changes := []string{}, []any{}, []string{}
	if req.Status != nil {
		if !incidentStatuses[*req.Status] {
			return httpx.BadRequest("status debe ser open, in_review o resolved")
		}
		sets = append(sets, "status = ?", "resolved_at = IF(? = 'resolved', COALESCE(resolved_at, CURRENT_TIMESTAMP(3)), NULL)")
		args, changes = append(args, *req.Status, *req.Status), append(changes, "estado a "+*req.Status)
	}
	if req.Resolution != nil {
		res, err := cleanResolution(*req.Resolution)
		if err != nil {
			return err
		}
		sets, args, changes = append(sets, "resolution = ?"), append(args, res), append(changes, "resolución")
	}
	if len(sets) == 0 {
		return httpx.BadRequest("Nada que actualizar")
	}
	id := r.PathValue("id")
	if _, err := h.DB.ExecContext(r.Context(), `UPDATE incidents SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		return err
	}
	i, err := scanIncident(h.DB.QueryRowContext(r.Context(), incidentQuery+` WHERE i.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	h.audit(r, "actualizó el incidente INC-%04d: %s", i.ID, strings.Join(changes, ", "))
	httpx.WriteJSON(w, http.StatusOK, i)
	return nil
}

func (h *Handler) ResolveIncident(w http.ResponseWriter, r *http.Request) error {
	res, err := h.DB.ExecContext(r.Context(), `UPDATE incidents SET status = 'resolved', resolved_at = CURRENT_TIMESTAMP(3)
		WHERE id = ? AND resolved_at IS NULL`, r.PathValue("id"))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	h.audit(r, "resolvió el incidente INC-%04s", r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// ListRampRequests is GET /api/v1/admin/ramp-requests?status=pending|acknowledged|expired.
func (h *Handler) ListRampRequests(w http.ResponseWriter, r *http.Request) error {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	rows, err := h.DB.QueryContext(r.Context(), `SELECT q.id, q.route_id, q.stop_id, st.name, q.status, q.created_at, q.acknowledged_at
		FROM ramp_requests q JOIN stops st ON st.id = q.stop_id WHERE q.status = ? ORDER BY q.id DESC LIMIT ?`, status, limitParam(r))
	if err != nil {
		return err
	}
	defer rows.Close()
	type ramp struct {
		ID             uint64     `json:"id"`
		RouteID        string     `json:"routeId"`
		StopID         string     `json:"stopId"`
		StopName       string     `json:"stopName"`
		Status         string     `json:"status"`
		CreatedAt      time.Time  `json:"createdAt"`
		AcknowledgedAt *time.Time `json:"acknowledgedAt"`
	}
	out := []ramp{}
	for rows.Next() {
		var q ramp
		if err := rows.Scan(&q.ID, &q.RouteID, &q.StopID, &q.StopName, &q.Status, &q.CreatedAt, &q.AcknowledgedAt); err != nil {
			return err
		}
		out = append(out, q)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return rows.Err()
}

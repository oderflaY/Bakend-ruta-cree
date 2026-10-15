package whatsapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Driver is the chofer a WhatsApp number belongs to.
type Driver struct {
	ID     string
	Name   string
	Active bool
}

// Draft holds the answers while the driver starts a route.
type Draft struct {
	Bus     string `json:"bus,omitempty"`
	RouteID string `json:"route,omitempty"`
	// Users on board, in the categories of the DIF's weekly log.
	Men        *int `json:"men,omitempty"`
	Women      *int `json:"women,omitempty"`
	Disability *int `json:"disability,omitempty"`
	Elderly    *int `json:"elderly,omitempty"`
	Pregnant   *int `json:"pregnant,omitempty"`
	Companions *int `json:"companions,omitempty"`
}

// Session is where a driver's conversation is. ShiftID is the route opened from this chat.
type Session struct {
	Phone    string
	DriverID string
	Step     string
	Draft    Draft
	ShiftID  uint64
}

// OpenShift is a route in progress, as the bot shows it.
type OpenShift struct {
	ID        uint64
	BusNumber string
	RouteID   string
	StartedAt time.Time
}

// Store is the bot's view of the database.
type Store interface {
	// DriverByPhone matches the last 10 digits, so "+52 1 618…" in WhatsApp equals "618…" in the panel.
	DriverByPhone(ctx context.Context, last10 string) (Driver, int, error)
	Session(ctx context.Context, phone string) (Session, error)
	SaveSession(ctx context.Context, s Session) error
	// BusStatus: "" when the bus is not in the fleet.
	BusStatus(ctx context.Context, number string) (string, error)
	// OpenShiftOf is the driver's route in progress (from the bot or the app), if any.
	OpenShiftOf(ctx context.Context, driverID string) (OpenShift, bool, error)
	// PhonesOnRoute are the chats with a route in progress on routeID.
	PhonesOnRoute(ctx context.Context, routeID string) ([]string, error)
}

type SQLStore struct{ DB *sql.DB }

func (s SQLStore) DriverByPhone(ctx context.Context, last10 string) (Driver, int, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, active FROM drivers
		WHERE phone <> '' AND RIGHT(REGEXP_REPLACE(phone, '[^0-9]', ''), 10) = ? LIMIT 2`, last10)
	if err != nil {
		return Driver{}, 0, err
	}
	defer rows.Close()
	var d Driver
	n := 0
	for rows.Next() {
		if err := rows.Scan(&d.ID, &d.Name, &d.Active); err != nil {
			return Driver{}, 0, err
		}
		n++
	}
	return d, n, rows.Err()
}

func (s SQLStore) Session(ctx context.Context, phone string) (Session, error) {
	out := Session{Phone: phone}
	var (
		draft string
		shift sql.NullInt64
	)
	err := s.DB.QueryRowContext(ctx, `SELECT driver_id, step, draft, shift_id FROM whatsapp_sessions WHERE phone = ?`, phone).
		Scan(&out.DriverID, &out.Step, &draft, &shift)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	_ = json.Unmarshal([]byte(draft), &out.Draft)
	out.ShiftID = uint64(shift.Int64)
	return out, nil
}

func (s SQLStore) SaveSession(ctx context.Context, x Session) error {
	draft, err := json.Marshal(x.Draft)
	if err != nil {
		return err
	}
	var shift any
	if x.ShiftID != 0 {
		shift = x.ShiftID
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO whatsapp_sessions (phone, driver_id, step, draft, shift_id) VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE driver_id = VALUES(driver_id), step = VALUES(step), draft = VALUES(draft), shift_id = VALUES(shift_id)`,
		x.Phone, x.DriverID, x.Step, string(draft), shift)
	return err
}

func (s SQLStore) BusStatus(ctx context.Context, number string) (string, error) {
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT status FROM buses WHERE number = ?`, number).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}

func (s SQLStore) OpenShiftOf(ctx context.Context, driverID string) (OpenShift, bool, error) {
	var o OpenShift
	err := s.DB.QueryRowContext(ctx, `SELECT s.id, b.number, s.route_id, s.started_at
		FROM shifts s JOIN buses b ON b.id = s.bus_id WHERE s.driver_id = ? AND s.ended_at IS NULL ORDER BY s.id DESC LIMIT 1`,
		driverID).Scan(&o.ID, &o.BusNumber, &o.RouteID, &o.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return o, false, nil
	}
	if err != nil {
		return o, false, err
	}
	return o, true, nil
}

func (s SQLStore) PhonesOnRoute(ctx context.Context, routeID string) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT ws.phone FROM whatsapp_sessions ws JOIN shifts s ON s.id = ws.shift_id
		WHERE s.ended_at IS NULL AND s.route_id = ?`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

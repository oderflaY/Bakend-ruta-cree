// Package live keeps the fleet's state in memory and fans it out over WebSockets.
package live

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// A bus without a position for this long disappears from the riders' map (its ETA would be wrong).
const staleAfter = 5 * time.Minute

// Shift is an open driver shift and, once reported, the bus's last position.
type Shift struct {
	ID        uint64
	DriverID  string
	BusID     uint64
	BusNumber string
	RouteID   string
	Ramp      bool

	lat, lon, speedKmh float64
	fixTime            time.Time
	hasFix             bool
	visible            bool
	lastAccepted       time.Time
}

type Hub struct {
	mu      sync.RWMutex
	shifts  map[uint64]*Shift // by shift id
	byBus   map[uint64]*Shift
	public  map[*client]struct{}
	drivers map[*client]uint64 // client -> shift id

	dirtyPos map[uint64]bool // bus ids with a new position since the last flush
	dirtyDev map[uint64]Device

	maxClients int
}

func NewHub(maxClients int) *Hub {
	return &Hub{
		shifts:     map[uint64]*Shift{},
		byBus:      map[uint64]*Shift{},
		public:     map[*client]struct{}{},
		drivers:    map[*client]uint64{},
		dirtyPos:   map[uint64]bool{},
		dirtyDev:   map[uint64]Device{},
		maxClients: maxClients,
	}
}

// ---- shifts ----

func (h *Hub) ShiftActive(id uint64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.shifts[id]
	return ok
}

func (h *Hub) Shift(id uint64) (Shift, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.shifts[id]
	if !ok {
		return Shift{}, false
	}
	return *s, true
}

// ShiftByBusNumber finds the open shift of a bus by its number (the Traccar device identifier).
func (h *Hub) ShiftByBusNumber(number string) (Shift, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.shifts {
		if strings.EqualFold(s.BusNumber, number) {
			return *s, true
		}
	}
	return Shift{}, false
}

// LastFix is the shift's latest reported position, if any.
func (h *Hub) LastFix(id uint64) (lat, lon float64, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if s, found := h.shifts[id]; found && s.hasFix {
		return s.lat, s.lon, true
	}
	return 0, 0, false
}

// LastFixTime is when the shift's bus last reported a position, from Traccar Client or from the app.
func (h *Hub) LastFixTime(id uint64) (time.Time, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if s, found := h.shifts[id]; found && s.hasFix {
		return s.fixTime, true
	}
	return time.Time{}, false
}

// AddShift registers an open shift. The bus shows up on the map with its first position.
func (h *Hub) AddShift(s Shift) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.byBus[s.BusID]; ok {
		delete(h.shifts, old.ID)
	}
	cp := s
	h.shifts[s.ID] = &cp
	h.byBus[s.BusID] = &cp
}

// RemoveShift hides the bus from riders (device without "ruta") and closes the driver's sockets.
func (h *Hub) RemoveShift(id uint64) {
	h.mu.Lock()
	s, ok := h.shifts[id]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(h.shifts, id)
	if h.byBus[s.BusID] == s {
		delete(h.byBus, s.BusID)
		h.dirtyDev[s.BusID] = hiddenDevice(s)
	}
	var toClose []*client
	for c, sid := range h.drivers {
		if sid == id {
			toClose = append(toClose, c)
		}
	}
	h.mu.Unlock()
	for _, c := range toClose {
		c.close("turno terminado")
	}
}

func (h *Hub) SetRamp(busID uint64, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s, found := h.byBus[busID]; found {
		s.Ramp = ok
		if s.visible {
			h.dirtyDev[busID] = visibleDevice(s)
			h.dirtyPos[busID] = true
		}
	}
}

// ActiveShifts is a snapshot for the admin dashboard.
func (h *Hub) ActiveShifts() []ShiftView {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]ShiftView, 0, len(h.shifts))
	for _, s := range h.shifts {
		v := ShiftView{ShiftID: s.ID, DriverID: s.DriverID, BusID: s.BusID, BusNumber: s.BusNumber, RouteID: s.RouteID, Ramp: s.Ramp}
		if s.hasFix {
			t := s.fixTime
			v.Lat, v.Lon, v.SpeedKmh, v.LastFix = &s.lat, &s.lon, &s.speedKmh, &t
		}
		out = append(out, v)
	}
	return out
}

type ShiftView struct {
	ShiftID   uint64     `json:"shiftId"`
	DriverID  string     `json:"driverId"`
	BusID     uint64     `json:"busId"`
	BusNumber string     `json:"busNumber"`
	RouteID   string     `json:"routeId"`
	Ramp      bool       `json:"rampOperational"`
	Lat       *float64   `json:"lat,omitempty"`
	Lon       *float64   `json:"lon,omitempty"`
	SpeedKmh  *float64   `json:"speedKmh,omitempty"`
	LastFix   *time.Time `json:"lastFix,omitempty"`
}

// ---- positions ----

// ErrTooFrequent: drivers may report at most once per second.
type TooFrequentError struct{}

func (TooFrequentError) Error() string { return "posición demasiado frecuente" }

// UpdatePosition stores the latest fix; it reaches riders on the next flush (≤1 s).
func (h *Hub) UpdatePosition(shiftID uint64, lat, lon, speedKmh float64, at time.Time) (Shift, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.shifts[shiftID]
	if !ok {
		return Shift{}, errShiftClosed
	}
	now := time.Now()
	if now.Sub(s.lastAccepted) < time.Second {
		return Shift{}, TooFrequentError{}
	}
	s.lastAccepted = now
	s.lat, s.lon, s.speedKmh, s.fixTime, s.hasFix = lat, lon, speedKmh, at, true
	if !s.visible {
		s.visible = true
		h.dirtyDev[s.BusID] = visibleDevice(s)
	}
	h.dirtyPos[s.BusID] = true
	return *s, nil
}

type shiftClosedError struct{}

func (shiftClosedError) Error() string { return "turno cerrado" }

var errShiftClosed = shiftClosedError{}

// ---- events ----

// Alarm broadcasts an incident immediately (not batched): riders must switch routes right away.
func (h *Hub) Alarm(incidentID, busID uint64, alarm string) {
	h.broadcastPublic(message{Events: []Event{{
		ID: incidentID, Type: "alarm", DeviceID: busID,
		Attributes: map[string]any{"alarm": alarm},
	}}})
}

// NotifyRamp sends a ramp request to every connected driver on the route; returns how many got it.
func (h *Hub) NotifyRamp(req RampRequest) int {
	payload, err := json.Marshal(message{RampRequests: []RampRequest{req}})
	if err != nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for c, sid := range h.drivers {
		if s, ok := h.shifts[sid]; ok && s.RouteID == req.RouteID {
			if c.send(payload) {
				n++
			}
		}
	}
	return n
}

func (h *Hub) NotifyRampAck(routeID string, id uint64) {
	payload, _ := json.Marshal(message{RampAcks: []uint64{id}})
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c, sid := range h.drivers {
		if s, ok := h.shifts[sid]; ok && s.RouteID == routeID {
			c.send(payload)
		}
	}
}

func (h *Hub) broadcastPublic(m message) {
	payload, err := json.Marshal(m)
	if err != nil {
		slog.Error("serializando mensaje", "error", err)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.public {
		c.send(payload)
	}
}

// Run flushes batched changes every second and hides stale buses. Encoding happens once per
// flush, not once per client.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.closeAll()
			return
		case <-t.C:
			h.flush()
		}
	}
}

func (h *Hub) flush() {
	h.mu.Lock()
	now := time.Now()
	for _, s := range h.byBus {
		if s.visible && now.Sub(s.fixTime) > staleAfter {
			s.visible = false
			h.dirtyDev[s.BusID] = hiddenDevice(s)
			delete(h.dirtyPos, s.BusID)
		}
	}
	if len(h.dirtyPos) == 0 && len(h.dirtyDev) == 0 {
		h.mu.Unlock()
		return
	}
	var m message
	for _, d := range h.dirtyDev {
		m.Devices = append(m.Devices, d)
	}
	for busID := range h.dirtyPos {
		if s, ok := h.byBus[busID]; ok && s.visible {
			m.Positions = append(m.Positions, positionOf(s))
		}
	}
	clear(h.dirtyDev)
	clear(h.dirtyPos)
	h.mu.Unlock()
	h.broadcastPublic(m)
}

// snapshot is what a rider receives on connect: every visible bus.
func (h *Hub) snapshot() []byte {
	h.mu.RLock()
	m := message{Devices: []Device{}, Positions: []Position{}}
	for _, s := range h.byBus {
		if s.visible {
			m.Devices = append(m.Devices, visibleDevice(s))
			m.Positions = append(m.Positions, positionOf(s))
		}
	}
	h.mu.RUnlock()
	b, _ := json.Marshal(m)
	return b
}

func visibleDevice(s *Shift) Device {
	return Device{ID: s.BusID, Name: "Unidad " + s.BusNumber, Attributes: map[string]any{"ruta": s.RouteID, "rampa": s.Ramp}}
}

// hiddenDevice has no "ruta": the app keeps the position but filters it out of every route.
func hiddenDevice(s *Shift) Device {
	return Device{ID: s.BusID, Name: "Unidad " + s.BusNumber, Attributes: map[string]any{"rampa": s.Ramp}}
}

func positionOf(s *Shift) Position {
	return Position{
		DeviceID: s.BusID, Latitude: s.lat, Longitude: s.lon,
		Speed:      s.speedKmh * knotsPerKmh,
		FixTime:    s.fixTime.UTC().Format(time.RFC3339),
		Attributes: map[string]any{"rampa": s.Ramp},
	}
}

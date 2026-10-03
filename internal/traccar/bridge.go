package traccar

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/eter/rutacree-backend/internal/live"
)

const (
	knotsToKmh = 1.852
	// Fixes older than this are history the phone uploads after being offline: they are not "now".
	maxFixAge = 2 * time.Minute
	// Throttle for reloading the device list when a position arrives from an unknown device.
	refreshEvery = 10 * time.Second
)

// Bridge feeds positions from Traccar into the hub. A position counts only while its bus (the
// Traccar device's uniqueId) has an open RutaCree shift: the shift decides route and visibility.
type Bridge struct {
	Client   *Client
	Hub      *live.Hub
	Recorder *live.Recorder
	// OsmAnd URL the driver types in Traccar Client (as reachable from the phone).
	OsmAndURL string

	mu          sync.Mutex
	uniqueByID  map[int64]string
	lastFix     map[string]time.Time // by upper-case bus number
	lastRefresh time.Time
	connected   bool
}

func NewBridge(client *Client, hub *live.Hub, recorder *live.Recorder, osmAndURL string) *Bridge {
	return &Bridge{
		Client: client, Hub: hub, Recorder: recorder, OsmAndURL: osmAndURL,
		uniqueByID: map[int64]string{}, lastFix: map[string]time.Time{},
	}
}

// Run keeps the Traccar stream open, logging in again and reconnecting with backoff.
func (b *Bridge) Run(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := b.connect(ctx)
		b.setConnected(false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second // it was a healthy session, not a crash loop
		}
		slog.Warn("conexión con Traccar perdida, reintentando", "error", err, "en", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (b *Bridge) connect(ctx context.Context) error {
	if err := b.Client.Login(ctx); err != nil {
		return err
	}
	if err := b.Client.DisableRegistration(ctx); err != nil {
		slog.Warn("no se pudo desactivar el registro abierto en Traccar", "error", err)
	}
	if err := b.refreshDevices(ctx); err != nil {
		return err
	}
	b.setConnected(true)
	slog.Info("conectado a Traccar")
	return b.Client.Stream(ctx, b.onDevices, func(ps []Position) { b.onPositions(ctx, ps) })
}

func (b *Bridge) setConnected(v bool) {
	b.mu.Lock()
	b.connected = v
	b.mu.Unlock()
}

// Connected reports whether the Traccar stream is currently open.
func (b *Bridge) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected
}

func (b *Bridge) refreshDevices(ctx context.Context) error {
	devices, err := b.Client.Devices(ctx)
	if err != nil {
		return err
	}
	b.onDevices(devices)
	b.mu.Lock()
	b.lastRefresh = time.Now()
	b.mu.Unlock()
	return nil
}

func (b *Bridge) onDevices(devices []Device) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, d := range devices {
		b.uniqueByID[d.ID] = strings.ToUpper(strings.TrimSpace(d.UniqueID))
	}
}

func (b *Bridge) onPositions(ctx context.Context, positions []Position) {
	for _, p := range positions {
		b.mu.Lock()
		unique, known := b.uniqueByID[p.DeviceID]
		stale := time.Since(b.lastRefresh) > refreshEvery
		b.mu.Unlock()
		if !known && stale {
			if err := b.refreshDevices(ctx); err != nil {
				slog.Warn("recargando dispositivos de Traccar", "error", err)
			}
			b.mu.Lock()
			unique, known = b.uniqueByID[p.DeviceID]
			b.mu.Unlock()
		}
		if !known || !p.Valid || time.Since(p.FixTime) > maxFixAge {
			continue
		}
		shift, ok := b.Hub.ShiftByBusNumber(unique)
		if !ok {
			continue
		}
		kmh := p.Speed * knotsToKmh
		updated, err := b.Hub.UpdatePosition(shift.ID, p.Latitude, p.Longitude, kmh, p.FixTime)
		if err != nil {
			continue // more than one fix per second, or the shift just closed
		}
		b.Recorder.Record(updated, p.Latitude, p.Longitude, kmh, p.FixTime)
		b.mu.Lock()
		b.lastFix[unique] = time.Now()
		b.mu.Unlock()
	}
}

// LastFix is when Traccar last delivered a usable position for the bus.
func (b *Bridge) LastFix(busNumber string) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.lastFix[strings.ToUpper(busNumber)]
	return t, ok
}

// EnsureDevice registers the bus in Traccar so the driver's phone is accepted as soon as it reports.
func (b *Bridge) EnsureDevice(ctx context.Context, busNumber string) error {
	d, err := b.Client.EnsureDevice(ctx, strings.ToUpper(busNumber), "Unidad "+busNumber)
	if err != nil {
		return err
	}
	b.onDevices([]Device{d})
	return nil
}

func (b *Bridge) PublicURL() string { return b.OsmAndURL }

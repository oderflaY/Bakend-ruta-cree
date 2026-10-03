package live

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

type fix struct {
	shiftID, busID uint64
	lat, lon, kmh  float64
	at             time.Time
}

// Recorder writes the position history in multi-row INSERTs. With a few hundred buses reporting
// every 5 s that's one statement every 2 s instead of dozens per second. If MariaDB falls behind,
// fixes are dropped from history only; the live map never waits on the database.
type Recorder struct {
	db      *sql.DB
	queue   chan fix
	dropped atomic.Int64
}

const (
	recorderQueue = 8192
	batchMax      = 500
	flushEvery    = 2 * time.Second
)

func NewRecorder(db *sql.DB) *Recorder {
	return &Recorder{db: db, queue: make(chan fix, recorderQueue)}
}

func (r *Recorder) Record(s Shift, lat, lon, kmh float64, at time.Time) {
	select {
	case r.queue <- fix{s.ID, s.BusID, lat, lon, kmh, at.UTC()}:
	default:
		if r.dropped.Add(1)%1000 == 1 {
			slog.Warn("historial de posiciones saturado, descartando", "descartadas", r.dropped.Load())
		}
	}
}

// Run flushes until ctx ends, then drains what's left.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	batch := make([]fix, 0, batchMax)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.insert(fctx, batch); err != nil {
			slog.Error("guardando posiciones", "n", len(batch), "error", err)
		}
		batch = batch[:0]
	}
	for {
		select {
		case f := <-r.queue:
			batch = append(batch, f)
			if len(batch) >= batchMax {
				flush()
			}
		case <-t.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case f := <-r.queue:
					batch = append(batch, f)
					if len(batch) >= batchMax {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (r *Recorder) insert(ctx context.Context, batch []fix) error {
	var sb strings.Builder
	sb.WriteString("INSERT INTO positions (shift_id, bus_id, lat, lon, speed_kmh, recorded_at) VALUES ")
	args := make([]any, 0, len(batch)*6)
	for i, f := range batch {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?,?,?,?,?,?)")
		args = append(args, f.shiftID, f.busID, f.lat, f.lon, f.kmh, f.at)
	}
	_, err := r.db.ExecContext(ctx, sb.String(), args...)
	return err
}

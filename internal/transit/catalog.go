package transit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/eter/rutacree-backend/internal/httpx"
)

// Catalog keeps active routes in memory: they change rarely and are read on every app start and
// every ramp request, so reads never touch the database.
type Catalog struct {
	db *sql.DB

	mu     sync.RWMutex
	routes map[string]*Route
	stops  map[string]*StopRef
	body   []byte // pre-encoded GET /api/v1/routes response
	etag   string
}

type StopRef struct {
	Stop
	RouteID string
}

func NewCatalog(db *sql.DB) *Catalog { return &Catalog{db: db} }

func (c *Catalog) Load(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, `SELECT geojson FROM routes WHERE active ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	routes := map[string]*Route{}
	stops := map[string]*StopRef{}
	list := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		r, err := ParseRoute(raw)
		if err != nil {
			return fmt.Errorf("ruta guardada inválida: %w", err)
		}
		routes[r.ID] = r
		for _, s := range r.Stops {
			stops[s.ID] = &StopRef{Stop: s, RouteID: r.ID}
		}
		list = append(list, r.GeoJSON)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"routes": list})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)

	c.mu.Lock()
	c.routes, c.stops, c.body, c.etag = routes, stops, body, `"`+hex.EncodeToString(sum[:12])+`"`
	c.mu.Unlock()
	return nil
}

// Upsert stores a validated route and its stops in one transaction, then refreshes the cache.
func (c *Catalog) Upsert(ctx context.Context, r *Route) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO routes (id, name, color, geojson, active) VALUES (?, ?, ?, ?, TRUE)
		ON DUPLICATE KEY UPDATE name = VALUES(name), color = VALUES(color), geojson = VALUES(geojson), active = TRUE`,
		r.ID, r.Name, r.Color, string(r.GeoJSON)); err != nil {
		return err
	}
	// Stops that disappeared from the GeoJSON are removed (their pending ramp requests cascade).
	ids := make([]any, 0, len(r.Stops)+1)
	ids = append(ids, r.ID)
	placeholders := bytes.Buffer{}
	for i, s := range r.Stops {
		if i > 0 {
			placeholders.WriteString(",")
		}
		placeholders.WriteString("?")
		ids = append(ids, s.ID)
		if _, err := tx.ExecContext(ctx, "INSERT INTO stops (id, route_id, name, lat, lon, `accessible`, seq) VALUES (?, ?, ?, ?, ?, ?, ?) "+
			"ON DUPLICATE KEY UPDATE route_id = VALUES(route_id), name = VALUES(name), lat = VALUES(lat), lon = VALUES(lon), "+
			"`accessible` = VALUES(`accessible`), seq = VALUES(seq)",
			s.ID, r.ID, s.Name, s.Lat, s.Lon, s.Accessible, s.Seq); err != nil {
			return err
		}
	}
	q := `DELETE FROM stops WHERE route_id = ?`
	if len(r.Stops) > 0 {
		q += ` AND id NOT IN (` + placeholders.String() + `)`
	}
	if _, err := tx.ExecContext(ctx, q, ids...); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return c.Load(ctx)
}

func (c *Catalog) Deactivate(ctx context.Context, id string) error {
	res, err := c.db.ExecContext(ctx, `UPDATE routes SET active = FALSE WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	return c.Load(ctx)
}

func (c *Catalog) Route(id string) (*Route, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.routes[id]
	return r, ok
}

func (c *Catalog) Stop(id string) (*StopRef, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s, ok := c.stops[id]
	return s, ok
}

func (c *Catalog) Empty() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.routes) == 0
}

// ServeRoutes answers GET /api/v1/routes: {"routes": [FeatureCollection, ...]}, with ETag so the
// app can revalidate cheaply on every start.
func (c *Catalog) ServeRoutes(w http.ResponseWriter, r *http.Request) error {
	c.mu.RLock()
	body, etag := c.body, c.etag
	c.mu.RUnlock()
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, err := w.Write(body)
	return err
}

// ServeRoute answers GET /api/v1/routes/{id}: the bare FeatureCollection, byte-compatible with the
// app's bundled files/routes/*.geojson.
func (c *Catalog) ServeRoute(w http.ResponseWriter, r *http.Request) error {
	route, ok := c.Route(r.PathValue("id"))
	if !ok {
		return httpx.ErrNotFound
	}
	w.Header().Set("Content-Type", "application/geo+json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	_, err := w.Write(route.GeoJSON)
	return err
}

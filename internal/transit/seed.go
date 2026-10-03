package transit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Seed imports the app's bundled data on first boot: seed/routes/*.geojson and seed/drivers.json.
// Drivers are created without a PIN (they can't start a shift until an admin sets one).
func Seed(ctx context.Context, db *sql.DB, c *Catalog, dir string) error {
	if dir == "" || !c.Empty() {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "routes", "*.geojson"))
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		r, err := ParseRoute(data)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if err := c.Upsert(ctx, r); err != nil {
			return err
		}
		slog.Info("ruta importada", "id", r.ID, "paradas", len(r.Stops))
	}

	data, err := os.ReadFile(filepath.Join(dir, "drivers.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var drivers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &drivers); err != nil {
		return fmt.Errorf("drivers.json: %w", err)
	}
	for _, d := range drivers {
		if _, err := db.ExecContext(ctx, `INSERT IGNORE INTO drivers (id, name) VALUES (?, ?)`, d.ID, d.Name); err != nil {
			return err
		}
	}
	slog.Info("choferes importados (sin PIN: asígnalo con `api driver-pin <id>` o desde el panel)", "n", len(drivers))
	return nil
}

package dispatch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eter/rutacree-backend/internal/db"
	"github.com/eter/rutacree-backend/internal/live"
	"github.com/eter/rutacree-backend/internal/transit"
)

// Needs a scratch MariaDB: RUTACREE_TEST_DSN='root:clave@tcp(127.0.0.1:3306)/rutacree?parseTime=true'.
func TestDriverReportsFillTheVehicleReport(t *testing.T) {
	dsn := os.Getenv("RUTACREE_TEST_DSN")
	if dsn == "" {
		t.Skip("RUTACREE_TEST_DSN no definido")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`DELETE FROM vehicle_reports`)
	mustExec(`INSERT IGNORE INTO drivers (id, name) VALUES ('CH-901', 'Hugo Alonso Estevane')`)
	mustExec(`INSERT IGNORE INTO buses (number) VALUES ('25-1451')`)
	var busID uint64
	if err := pool.QueryRowContext(ctx, `SELECT id FROM buses WHERE number = '25-1451'`).Scan(&busID); err != nil {
		t.Fatal(err)
	}

	loc, _ := time.LoadLocation("America/Monterrey")
	s := &Service{DB: pool, Catalog: transit.NewCatalog(pool), Location: loc}
	shift := live.Shift{ID: 1, DriverID: "CH-901", BusID: busID, BusNumber: "25-1451", RouteID: "cas-3"}
	// 23:30 UTC is still the same local day (17:30 in Durango).
	at := time.Date(2026, 10, 13, 23, 30, 0, 0, time.UTC)
	if err := s.noteOnVehicleReport(ctx, shift, "ramp_failure", at); err != nil {
		t.Fatal(err)
	}
	if err := s.noteOnVehicleReport(ctx, shift, "breakdown", at.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// One the panel wrote by hand that day stays as it is.
	mustExec(`INSERT IGNORE INTO admins (id, username, password_hash, role) VALUES (1, 'prueba', 'x', 'superadmin')`)
	mustExec(`INSERT INTO vehicle_reports (bus_id, day, observations, created_by) VALUES (?, '2026-10-13', 'LLANTA BAJA', 1)`, busID)

	rows, err := pool.QueryContext(ctx, `SELECT DATE_FORMAT(day, '%Y-%m-%d'), observations, created_by IS NULL FROM vehicle_reports ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var day, obs string
		var system bool
		if err := rows.Scan(&day, &obs, &system); err != nil {
			t.Fatal(err)
		}
		got = append(got, day+"|"+obs)
		if !system && obs != "LLANTA BAJA" {
			t.Errorf("tocó un reporte escrito a mano: %q", obs)
		}
	}
	if len(got) != 2 {
		t.Fatalf("se esperaban 2 reportes (sistema + panel), hay %d: %q", len(got), got)
	}
	want := "2026-10-13|17:30 FALLA DE RAMPA REPORTADA POR EL CHOFER HUGO ALONSO ESTEVANE EN CAS-3.\n" +
		"17:50 FALLA DEL CAMIÓN REPORTADA POR EL CHOFER HUGO ALONSO ESTEVANE EN CAS-3."
	if got[0] != want {
		t.Errorf("reporte del sistema:\n got %q\nwant %q", got[0], want)
	}
	if !strings.HasPrefix(got[1], "2026-10-13|LLANTA") {
		t.Errorf("reporte del panel: %q", got[1])
	}
}

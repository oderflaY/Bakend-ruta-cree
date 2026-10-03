// Package db opens the MariaDB pool and applies the embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open connects with retries: in docker-compose the API can start while MariaDB is still booting.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	pool, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(25)
	pool.SetMaxIdleConns(10)
	pool.SetConnMaxLifetime(30 * time.Minute)
	pool.SetConnMaxIdleTime(5 * time.Minute)

	delay := time.Second
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.PingContext(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if attempt == 10 {
			pool.Close()
			return nil, fmt.Errorf("no se pudo conectar a MariaDB: %w", err)
		}
		slog.Warn("MariaDB no disponible todavía, reintentando", "intento", attempt, "error", err)
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 10*time.Second)
	}
}

// Migrate applies pending migrations in order. A named lock keeps two API replicas from racing.
func Migrate(ctx context.Context, pool *sql.DB) error {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var locked int
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('rutacree_migrate', 30)").Scan(&locked); err != nil || locked != 1 {
		return fmt.Errorf("no se obtuvo el candado de migraciones: %v", err)
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('rutacree_migrate')")

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(100) NOT NULL PRIMARY KEY,
		applied_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)) ENGINE=InnoDB`); err != nil {
		return err
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		// multiStatements is off in the DSN (it widens SQL injection impact), so run one by one.
		for _, stmt := range splitStatements(string(body)) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migración %s: %w", name, err)
			}
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations (version) VALUES (?)", name); err != nil {
			return err
		}
		slog.Info("migración aplicada", "version", name)
	}
	return nil
}

func splitStatements(sqlText string) []string {
	var lines []string
	for _, line := range strings.Split(sqlText, "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "--") {
			lines = append(lines, line)
		}
	}
	var out []string
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		if s := strings.TrimSpace(stmt); s != "" {
			out = append(out, s)
		}
	}
	return out
}

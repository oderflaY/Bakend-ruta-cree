// Package config reads the server settings from the environment.
//
// Every secret can also be given as a file (VAR_FILE=/run/secrets/...), which is how
// docker-compose passes them, so they never show up in `docker inspect` or the process env.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr string

	DBHost     string
	DBPort     string
	DBName     string
	DBUser     string
	DBPassword string

	JWTSecret      []byte
	AdminTokenTTL  time.Duration
	DriverTokenTTL time.Duration
	RiderTokenTTL  time.Duration

	// Traccar (optional). With TraccarURL set, bus positions come from Traccar Client on the
	// driver's phone; TraccarOsmAndURL is the address the phone reaches (shown to the driver).
	TraccarURL       string
	TraccarEmail     string
	TraccarPassword  string
	TraccarOsmAndURL string

	// Bootstrap admin, created only when the admins table is empty.
	AdminEmail    string
	AdminPassword string

	// Origins allowed to open the WebSocket / call the API from a browser (the admin dashboard).
	// Native apps send no Origin header and are not affected.
	AllowedOrigins []string
	// Proxies whose X-Forwarded-For is trusted (e.g. the Caddy container network).
	TrustedProxies []*net.IPNet

	SeedDir string
	// Control panel's static files (Frontend admin), served at /panel/. Empty = not served.
	PanelDir          string
	PositionRetention time.Duration
	MaxSocketClients  int
}

func Load() (*Config, error) {
	c := &Config{
		Addr:              env("API_ADDR", ":8080"),
		DBHost:            env("DB_HOST", "db"),
		DBPort:            env("DB_PORT", "3306"),
		DBName:            env("DB_NAME", "rutacree"),
		DBUser:            env("DB_USER", "rutacree"),
		AdminEmail:        strings.ToLower(strings.TrimSpace(env("ADMIN_EMAIL", ""))),
		SeedDir:           env("SEED_DIR", ""),
		PanelDir:          env("PANEL_DIR", ""),
		AllowedOrigins:    splitList(env("ALLOWED_ORIGINS", "")),
		AdminTokenTTL:     8 * time.Hour,
		DriverTokenTTL:    14 * time.Hour,
		RiderTokenTTL:     30 * 24 * time.Hour,
		TraccarURL:        env("TRACCAR_URL", ""),
		TraccarEmail:      strings.ToLower(strings.TrimSpace(env("TRACCAR_EMAIL", ""))),
		TraccarOsmAndURL:  env("TRACCAR_PUBLIC_OSMAND_URL", ""),
		PositionRetention: 30 * 24 * time.Hour,
		MaxSocketClients:  5000,
	}

	var err error
	if c.DBPassword, err = secret("DB_PASSWORD"); err != nil {
		return nil, err
	}
	jwt, err := secret("JWT_SECRET")
	if err != nil {
		return nil, err
	}
	if len(jwt) < 32 {
		return nil, errors.New("JWT_SECRET debe tener al menos 32 caracteres")
	}
	c.JWTSecret = []byte(jwt)
	if c.AdminPassword, err = secretOptional("ADMIN_PASSWORD"); err != nil {
		return nil, err
	}
	if c.AdminEmail != "" && len(c.AdminPassword) < 12 {
		return nil, errors.New("ADMIN_PASSWORD debe tener al menos 12 caracteres")
	}

	if c.TraccarPassword, err = secretOptional("TRACCAR_PASSWORD"); err != nil {
		return nil, err
	}
	if c.TraccarURL != "" && (c.TraccarEmail == "" || len(c.TraccarPassword) < 12) {
		return nil, errors.New("con TRACCAR_URL hacen falta TRACCAR_EMAIL y TRACCAR_PASSWORD (mínimo 12 caracteres)")
	}

	if v := os.Getenv("POSITION_RETENTION_DAYS"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || days < 1 {
			return nil, fmt.Errorf("POSITION_RETENTION_DAYS inválido: %q", v)
		}
		c.PositionRetention = time.Duration(days) * 24 * time.Hour
	}
	if v := os.Getenv("MAX_SOCKET_CLIENTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("MAX_SOCKET_CLIENTS inválido: %q", v)
		}
		c.MaxSocketClients = n
	}
	for _, cidr := range splitList(env("TRUSTED_PROXIES", "")) {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, n)
	}
	return c, nil
}

// DSN for go-sql-driver/mysql. utf8mb4 so names with accents and emoji round-trip; UTC everywhere.
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s)/%s?parseTime=true&loc=UTC&charset=utf8mb4&collation=utf8mb4_unicode_ci"+
			"&timeout=5s&readTimeout=15s&writeTimeout=15s&multiStatements=false&interpolateParams=false",
		c.DBUser, c.DBPassword, net.JoinHostPort(c.DBHost, c.DBPort), c.DBName,
	)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func secret(key string) (string, error) {
	v, err := secretOptional(key)
	if err == nil && v == "" {
		err = fmt.Errorf("falta %s (o %s_FILE)", key, key)
	}
	return v, err
}

func secretOptional(key string) (string, error) {
	if path := os.Getenv(key + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("leyendo %s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(key), nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Command api is the RutaCree backend.
//
//	api                  run the server (default)
//	api healthcheck      exit 0 if the local server answers /healthz (used by Docker)
//	api driver-login <id> <email> [name]  set (or create, with a name) a driver's login; the password
//	                                      is read from stdin
//	                               (never from argv: it would show in ps/history)
//	api publicar-ruta <geojson>...  write routes to the database
//	api importar-flota <xlsx> [--baja-resto]  load the DIF fleet spreadsheet into buses
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the image has no zoneinfo; the bot writes local times

	"github.com/eter/rutacree-backend/internal/admin"
	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/config"
	"github.com/eter/rutacree-backend/internal/db"
	"github.com/eter/rutacree-backend/internal/dispatch"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/live"
	"github.com/eter/rutacree-backend/internal/login"
	"github.com/eter/rutacree-backend/internal/riders"
	"github.com/eter/rutacree-backend/internal/server"
	"github.com/eter/rutacree-backend/internal/traccar"
	"github.com/eter/rutacree-backend/internal/transit"
	"github.com/eter/rutacree-backend/internal/whatsapp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "driver-login":
		if len(os.Args) != 4 && len(os.Args) != 5 {
			err = errors.New("uso: api driver-login <id-del-chofer> <correo> [\"Nombre\"]  (la contraseña se lee de la entrada estándar;\n" +
				"con nombre, crea al chofer si no existe)")
		} else {
			name := ""
			if len(os.Args) == 5 {
				name = os.Args[4]
			}
			err = setDriverLogin(os.Args[2], os.Args[3], name)
		}
	case "importar-flota":
		if len(os.Args) < 3 || len(os.Args) > 4 || (len(os.Args) == 4 && os.Args[3] != "--baja-resto") {
			err = errors.New("uso: api importar-flota <parque-vehicular.xlsx> [--baja-resto]  (--baja-resto da de baja las unidades que no vienen en el Excel)")
		} else {
			err = importFleet(os.Args[2], len(os.Args) == 4)
		}
	case "publicar-ruta":
		if len(os.Args) < 3 {
			err = errors.New("uso: api publicar-ruta <ruta.geojson>...  (el API la toma al reiniciarse)")
		} else {
			err = publishRoutes(os.Args[2:])
		}
	default:
		err = fmt.Errorf("comando desconocido: %s", cmd)
	}
	if err != nil {
		slog.Error("fallo", "error", err)
		os.Exit(1)
	}
}

func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	catalog := transit.NewCatalog(pool)
	if err := catalog.Load(ctx); err != nil {
		return err
	}
	if err := transit.Seed(ctx, pool, catalog, cfg.SeedDir); err != nil {
		return fmt.Errorf("importando datos iniciales: %w", err)
	}
	if created, err := admin.EnsureAdmin(ctx, pool, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		return err
	} else if created {
		slog.Info("administrador inicial creado", "email", cfg.AdminEmail)
	}

	hub := live.NewHub(cfg.MaxSocketClients)
	recorder := live.NewRecorder(pool)
	tokens := auth.NewTokens(cfg.JWTSecret)
	svc := &dispatch.Service{DB: pool, Hub: hub, Catalog: catalog, Tokens: tokens, Recorder: recorder, TokenTTL: cfg.DriverTokenTTL,
		WhatsApp: cfg.DispatchWhatsApp}
	if err := svc.LoadActiveShifts(ctx); err != nil {
		return err
	}

	runs := []func(context.Context){
		hub.Run,
		recorder.Run,
		func(c context.Context) { svc.RunJanitor(c, cfg.PositionRetention) },
	}
	if cfg.TraccarURL != "" {
		client, err := traccar.NewClient(cfg.TraccarURL, cfg.TraccarEmail, cfg.TraccarPassword)
		if err != nil {
			return err
		}
		bridge := traccar.NewBridge(client, hub, recorder, cfg.TraccarOsmAndURL)
		svc.Tracker = bridge
		runs = append(runs, bridge.Run)
		slog.Info("Traccar habilitado", "url", cfg.TraccarURL, "osmand", cfg.TraccarOsmAndURL)
	}

	// Background workers stop after the HTTP server has drained.
	workers, stopWorkers := context.WithCancel(context.Background())
	done := make(chan struct{}, len(runs))
	for _, run := range runs {
		go func() { run(workers); done <- struct{}{} }()
	}

	localZone, err := time.LoadLocation(cfg.LocalTimeZone)
	if err != nil {
		stopWorkers()
		return fmt.Errorf("ZONA_HORARIA: %w", err)
	}
	svc.Location = localZone // nothing reports before the HTTP server and the bot start below
	var waHook *whatsapp.Webhook
	if cfg.WhatsAppPhoneID != "" {
		loc := localZone
		bot := &whatsapp.Bot{
			Store:    whatsapp.SQLStore{DB: pool},
			Dispatch: svc,
			Send:     whatsapp.NewClient(cfg.WhatsAppAPIBase, cfg.WhatsAppPhoneID, cfg.WhatsAppToken),
			Location: loc,
			Routes: func() []whatsapp.RouteInfo {
				var out []whatsapp.RouteInfo
				for _, r := range catalog.Routes() {
					out = append(out, whatsapp.RouteInfo{ID: r.ID, Name: r.Name})
				}
				return out
			},
		}
		svc.OnRamp = func(req live.RampRequest) int { return bot.NotifyRamp(workers, req) }
		waHook = &whatsapp.Webhook{Bot: bot, VerifyToken: cfg.WhatsAppVerifyToken, AppSecret: cfg.WhatsAppAppSecret, Ctx: workers}
		slog.Info("chatbot de WhatsApp habilitado", "numero", cfg.WhatsAppPhoneID)
	}

	ips := httpx.NewClientIP(cfg.TrustedProxies)
	riderHandler := &riders.Handler{DB: pool, Tokens: tokens, TokenTTL: cfg.RiderTokenTTL}
	handler := server.New(workers, server.Deps{
		DB: pool, Hub: hub, Catalog: catalog, Tokens: tokens, Dispatch: svc, IPs: ips, AllowedOrigins: cfg.AllowedOrigins, PanelDir: cfg.PanelDir,
		WhatsApp: waHook,
		Admin: &admin.Handler{DB: pool, Hub: hub, Catalog: catalog, Tokens: tokens, Dispatch: svc, TokenTTL: cfg.AdminTokenTTL,
			Location: localZone, Routing: admin.NewRouter(cfg.RoutingURL)},
		Riders: riderHandler,
		Login:  &login.Handler{DB: pool, Tokens: tokens, Riders: riderHandler, DriverTTL: cfg.DriverTokenTTL},
	})
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// Read/write deadlines are set per request (httpx.Deadlines) so WebSockets can stay open.
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("API escuchando", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		stopWorkers()
		return err
	case <-ctx.Done():
	}
	slog.Info("apagando…")
	stopWorkers() // closes sockets (hub) and flushes pending positions (recorder)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	for range runs {
		<-done
	}
	return err
}

func healthcheck() error {
	port := "8080"
	if addr := os.Getenv("API_ADDR"); addr != "" {
		port = addr[strings.LastIndex(addr, ":")+1:]
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("healthz: %d", resp.StatusCode)
	}
	return nil
}

func setDriverLogin(id, rawEmail, name string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	name = strings.TrimSpace(name)
	if !dispatch.DriverIDPattern.MatchString(id) {
		return errors.New("id inválido (A-Z, 0-9 y guiones, máx. 16)")
	}
	if len(name) > 120 {
		return errors.New("nombre demasiado largo (máx. 120)")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	email, err := riders.NormalizeEmail(rawEmail)
	if err != nil {
		return errors.New("correo inválido")
	}
	fmt.Fprint(os.Stderr, "Contraseña (8 a 128 caracteres): ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	password := strings.TrimRight(line, "\r\n")
	if n := len([]rune(password)); n < riders.MinPassword || n > riders.MaxPassword {
		return errors.New("la contraseña debe tener entre 8 y 128 caracteres")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()
	var taken int
	if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM riders WHERE email = ?`, email).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return errors.New("ese correo ya pertenece a una cuenta de pasajero")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if name != "" {
		// Creates the driver, or renames and reactivates an existing one.
		_, err = pool.ExecContext(ctx, `INSERT INTO drivers (id, name, email, password_hash) VALUES (?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE name = VALUES(name), email = VALUES(email), password_hash = VALUES(password_hash),
			active = TRUE, failed_logins = 0, locked_until = NULL`, id, name, email, hash)
		if err != nil {
			return fmt.Errorf("guardando al chofer (¿el correo ya lo usa otro chofer?): %w", err)
		}
		fmt.Fprintln(os.Stderr, "Chofer listo:", id, name, "("+email+")")
		return nil
	}
	res, err := pool.ExecContext(ctx, `UPDATE drivers SET email = ?, password_hash = ?, failed_logins = 0, locked_until = NULL
		WHERE id = ?`, email, hash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no existe el chofer %s (agrega su nombre para crearlo)", id)
	}
	fmt.Fprintln(os.Stderr, "Acceso actualizado para", id, "("+email+")")
	return nil
}

// publishRoutes writes route GeoJSON files to the database, like PUT /api/v1/admin/routes but from the
// server's shell (no panel login). A running API picks them up when restarted.
func publishRoutes(files []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()
	catalog := transit.NewCatalog(pool)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		r, err := transit.ParseRoute(data)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if err := catalog.Upsert(ctx, r); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		fmt.Fprintf(os.Stderr, "Ruta publicada: %s (%s, %d paradas)\n", r.ID, r.Name, len(r.Stops))
	}
	return nil
}

// importFleet loads the DIF fleet spreadsheet (PARQUE VEHICULAR) into buses, like the panel's import.
func importFleet(file string, retireOthers bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	fleet, err := admin.ParseFleet(f)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DSN())
	if err != nil {
		return err
	}
	defer pool.Close()
	res, err := admin.ImportFleet(ctx, pool, fleet, retireOthers)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Parque vehicular: %d nuevas, %d actualizadas, %d dadas de baja\n", len(res.Created), len(res.Updated), len(res.Retired))
	return nil
}

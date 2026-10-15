// Package server wires handlers, middleware and rate limits into one http.Handler.
package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"time"

	"github.com/eter/rutacree-backend/internal/admin"
	"github.com/eter/rutacree-backend/internal/auth"
	"github.com/eter/rutacree-backend/internal/dispatch"
	"github.com/eter/rutacree-backend/internal/httpx"
	"github.com/eter/rutacree-backend/internal/live"
	"github.com/eter/rutacree-backend/internal/login"
	riderapi "github.com/eter/rutacree-backend/internal/riders"
	"github.com/eter/rutacree-backend/internal/transit"
	"github.com/eter/rutacree-backend/internal/whatsapp"
)

type Deps struct {
	DB             *sql.DB
	Hub            *live.Hub
	Catalog        *transit.Catalog
	Tokens         *auth.Tokens
	Dispatch       *dispatch.Service
	Admin          *admin.Handler
	Riders         *riderapi.Handler
	Login          *login.Handler
	IPs            *httpx.ClientIP
	AllowedOrigins []string
	// Folder with the control panel's static files; empty = not served.
	PanelDir string
	// Drivers' WhatsApp chatbot; nil when not configured.
	WhatsApp *whatsapp.Webhook
}

func New(ctx context.Context, d Deps) http.Handler {
	h := httpx.Handle
	// Rate limits per client IP. Mobile carriers put many phones behind one IP (CGNAT), so rider
	// limits are generous; logins are the tightest, and accounts also lock after 5 failures.
	var (
		general    = httpx.NewRateLimiter(ctx, 600, 120).Middleware(d.IPs)
		loginLimit = httpx.NewRateLimiter(ctx, 10, 5).Middleware(d.IPs)
		riders     = httpx.NewRateLimiter(ctx, 30, 10).Middleware(d.IPs)  // ramp requests and reports
		sockets    = httpx.NewRateLimiter(ctx, 120, 40).Middleware(d.IPs) // reconnect storms
	)
	driver := d.Tokens.Require(auth.RoleDriver, d.Hub)
	rider := d.Tokens.Require(auth.RoleRider, nil)
	driverAccount := d.Tokens.Require(auth.RoleDriverAccount, nil)
	adm := d.Tokens.Require(auth.RoleAdmin, nil)
	with := func(hd http.Handler, mw ...httpx.Middleware) http.Handler { return httpx.Chain(hd, mw...) }

	mux := http.NewServeMux()

	// Health: /healthz = process alive, /readyz = database reachable.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("GET /readyz", h(func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.PingContext(ctx); err != nil {
			return httpx.NewError(http.StatusServiceUnavailable, "db_down", "Base de datos no disponible")
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))

	// The WebSocket library matches Origin by host, CORS by full origin.
	var originHosts []string
	for _, o := range d.AllowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			originHosts = append(originHosts, u.Host)
		}
	}
	socket := &live.SocketHandler{Hub: d.Hub, OriginPatterns: originHosts, Tokens: d.Tokens, IPs: d.IPs, PerIP: 200}

	// Riders (no account).
	mux.Handle("GET /api/socket", with(http.HandlerFunc(socket.ServePublic), sockets))
	mux.Handle("GET /api/v1/routes", with(h(d.Catalog.ServeRoutes), general))
	mux.Handle("GET /api/v1/routes/{id}", with(h(d.Catalog.ServeRoute), general))
	mux.Handle("POST /api/v1/ramp-requests", with(h(d.Dispatch.RequestRamp), riders))
	mux.Handle("GET /api/v1/ramp-requests/{id}", with(h(d.Dispatch.RampStatus), general))
	mux.Handle("POST /api/v1/reports", with(h(d.Dispatch.CreateReport), riders))

	// One sign-in for everybody: the server answers with the role (driver or rider).
	mux.Handle("POST /api/v1/auth/login", with(h(d.Login.Login), loginLimit))

	// Optional rider accounts (profile sync). Sign-up shares the tight login limit.
	rd := d.Riders
	mux.Handle("POST /api/v1/riders", with(h(rd.Register), loginLimit))
	mux.Handle("GET /api/v1/riders/me", with(h(rd.Me), general, rider))
	mux.Handle("PUT /api/v1/riders/me", with(h(rd.UpdateProfile), general, rider))
	mux.Handle("DELETE /api/v1/riders/me", with(h(rd.Delete), general, rider))

	// Drivers.
	mux.Handle("GET /api/v1/driver/me", with(h(d.Login.DriverMe), general, driverAccount))
	mux.Handle("POST /api/v1/driver/shifts", with(h(d.Dispatch.StartShift), general, driverAccount))
	mux.Handle("POST /api/v1/driver/shifts/end", with(h(d.Dispatch.EndShift), general, driver))
	mux.Handle("POST /api/v1/driver/positions", with(h(d.Dispatch.ReportPosition), general, driver))
	mux.Handle("GET /api/v1/driver/osmand", with(h(d.Dispatch.OsmAnd), general, driver))
	mux.Handle("POST /api/v1/driver/osmand", with(h(d.Dispatch.OsmAnd), general, driver))
	mux.Handle("POST /api/v1/driver/incidents", with(h(d.Dispatch.ReportIncident), general, driver))
	mux.Handle("GET /api/v1/driver/tracker", with(h(d.Dispatch.TrackerStatus), general, driver))
	mux.Handle("POST /api/v1/driver/ramp-requests/{id}/ack", with(h(d.Dispatch.AckRamp), general, driver))
	mux.Handle("GET /api/v1/driver/socket", with(socket.ServeDriver(d.Dispatch.PendingRamps), sockets, driver))

	// Drivers' WhatsApp chatbot: Meta calls this URL (signed with the app secret).
	if d.WhatsApp != nil {
		mux.Handle("GET /api/whatsapp/webhook", with(http.HandlerFunc(d.WhatsApp.Verify), general))
		mux.Handle("POST /api/whatsapp/webhook", with(http.HandlerFunc(d.WhatsApp.Receive), general))
	}

	// Admin dashboard. Every route names the panel roles allowed to use it (see admin.Role).
	a := d.Admin
	mux.Handle("POST /api/v1/admin/login", with(h(a.Login), loginLimit))
	all := []admin.Role{admin.SuperAdmin, admin.Dispatcher, admin.Mechanic}
	sa := []admin.Role{admin.SuperAdmin}
	desk := []admin.Role{admin.SuperAdmin, admin.Dispatcher}
	fleet := []admin.Role{admin.SuperAdmin, admin.Mechanic}
	for _, rt := range []struct {
		pattern string
		fn      httpx.HandlerFunc
		roles   []admin.Role
	}{
		{"GET /api/v1/admin/me", a.Me, all},
		{"GET /api/v1/admin/overview", a.Overview, all},
		{"GET /api/v1/admin/shifts/active", a.ActiveShifts, desk},
		{"GET /api/v1/admin/shifts", a.ListShifts, desk},
		{"GET /api/v1/admin/drivers", a.ListDrivers, desk},
		{"POST /api/v1/admin/drivers", a.CreateDriver, sa},
		{"PATCH /api/v1/admin/drivers/{id}", a.UpdateDriver, sa},
		{"GET /api/v1/admin/buses", a.ListBuses, all},
		{"POST /api/v1/admin/buses", a.CreateBus, sa},
		{"PATCH /api/v1/admin/buses/{id}", a.UpdateBus, fleet},
		{"GET /api/v1/admin/buses/{id}/track", a.Track, desk},
		{"GET /api/v1/admin/assignments", a.ListAssignments, desk},
		{"POST /api/v1/admin/assignments", a.CreateAssignment, desk},
		{"DELETE /api/v1/admin/assignments/{id}", a.DeleteAssignment, desk},
		{"PUT /api/v1/admin/routes", a.PutRoute, sa},
		{"DELETE /api/v1/admin/routes/{id}", a.DeleteRoute, sa},
		{"GET /api/v1/admin/reports", a.ListReports, desk},
		{"PATCH /api/v1/admin/reports/{id}", a.UpdateReport, sa},
		{"GET /api/v1/admin/incidents", a.ListIncidents, desk},
		{"PATCH /api/v1/admin/incidents/{id}", a.UpdateIncident, sa},
		{"POST /api/v1/admin/incidents/{id}/resolve", a.ResolveIncident, sa},
		{"GET /api/v1/admin/ramp-requests", a.ListRampRequests, desk},
		{"GET /api/v1/admin/users", a.ListUsers, sa},
		{"POST /api/v1/admin/users", a.CreateUser, sa},
		{"PATCH /api/v1/admin/users/{id}", a.UpdateUser, sa},
		{"GET /api/v1/admin/audit", a.Audit, sa},
		{"GET /api/v1/admin/riders", a.ListRiders, sa},
		{"PATCH /api/v1/admin/riders/{id}", a.UpdateRider, sa},
		{"DELETE /api/v1/admin/riders/{id}", a.DeleteRider, sa},
		{"GET /api/v1/admin/maintenance", a.ListMaintenance, fleet},
		{"POST /api/v1/admin/maintenance", a.OpenMaintenance, fleet},
		{"PATCH /api/v1/admin/maintenance/{id}", a.UpdateMaintenance, fleet},
		{"GET /api/v1/admin/maintenance/{id}/orden", a.OrdenServicio, fleet},
		{"GET /api/v1/admin/vehicle-reports", a.ListVehicleReports, all},
		{"POST /api/v1/admin/vehicle-reports", a.CreateVehicleReport, all},
		{"GET /api/v1/admin/vehicle-reports/{id}/xlsx", a.VehicleReportXLSX, all},
		{"GET /api/v1/admin/formatos/bitacora", a.Bitacora, desk},
		// The DIF's forms, editable in the panel; each kind checks its own roles.
		{"GET /api/v1/admin/formatos/{kind}/{ref}", a.GetForm, all},
		{"PUT /api/v1/admin/formatos/{kind}/{ref}", a.SaveForm, all},
		{"GET /api/v1/admin/formatos/{kind}/{ref}/xlsx", a.FormXLSX, all},
	} {
		mux.Handle(rt.pattern, with(h(rt.fn), general, adm, a.Allow(rt.roles...)))
	}

	// The control panel (Frontend admin), served from the API's own origin: no CORS, and the
	// WebSocket's same-origin check passes without listing the panel in ALLOWED_ORIGINS.
	if d.PanelDir != "" {
		mux.Handle("GET /panel/", http.StripPrefix("/panel/", httpx.Static(d.PanelDir)))
		mux.Handle("GET /{$}", http.RedirectHandler("/panel/", http.StatusFound))
	}

	return httpx.Chain(mux,
		httpx.Recover,
		httpx.Logger(d.IPs),
		httpx.Deadlines(15*time.Second, 30*time.Second, "/api/socket", "/api/v1/driver/socket"),
		httpx.SecurityHeaders,
		httpx.CORS(d.AllowedOrigins),
	)
}

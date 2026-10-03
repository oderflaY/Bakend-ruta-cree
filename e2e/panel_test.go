//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Control panel: accounts with roles, fleet, scheduled shifts, report follow-up, audit and the
// static panel itself. Needs E2E_ADMIN_PASSWORD (the bootstrap SuperAdmin).

func callList(t *testing.T, path, token string) (int, []map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", base+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func must(t *testing.T, what string, got, want int, body map[string]any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: esperaba %d, llegó %d %v", what, want, got, body)
	}
}

func adminLogin(t *testing.T, email, password string) string {
	t.Helper()
	return loginAt(t, "/api/v1/admin/login", email, password)
}

func loginAt(t *testing.T, path, email, password string) string {
	t.Helper()
	for range 12 { // the login limit is real protection; wait it out instead of failing
		st, r := call(t, "POST", path, "", map[string]any{"email": email, "password": password})
		if st == 429 && r["error"] != "locked" {
			time.Sleep(10 * time.Second)
			continue
		}
		must(t, "login "+email, st, 200, r)
		return r["token"].(string)
	}
	t.Fatal("login: límite de intentos")
	return ""
}

func TestPanel(t *testing.T) {
	pass := os.Getenv("E2E_ADMIN_PASSWORD")
	if pass == "" {
		t.Skip("define E2E_ADMIN_PASSWORD para probar el panel")
	}
	sa := adminLogin(t, envOr("E2E_ADMIN_EMAIL", "admin@rutacree.mx"), pass)
	tag := fmt.Sprintf("%05d", rand.IntN(100000))

	st, me := call(t, "GET", "/api/v1/admin/me", sa, nil)
	must(t, "me", st, 200, me)
	if me["role"] != "superadmin" {
		t.Fatalf("el admin inicial debe ser superadmin: %v", me)
	}

	// Accounts: a dispatcher and a mechanic.
	accounts := map[string]string{}
	for _, role := range []string{"dispatcher", "mechanic"} {
		email := fmt.Sprintf("%s-%s@rutacree.mx", role, tag)
		st, u := call(t, "POST", "/api/v1/admin/users", sa, map[string]any{
			"name": "Prueba " + role, "username": role + "_" + tag, "email": email, "role": role, "password": "clave-de-prueba-1"})
		must(t, "crear "+role, st, 201, u)
		accounts[role] = adminLogin(t, email, "clave-de-prueba-1")
		accounts[role+"-id"] = fmt.Sprint(u["id"])
	}
	disp, mech := accounts["dispatcher"], accounts["mechanic"]

	// Roles are enforced by the server, not just hidden by the panel.
	for _, c := range []struct {
		token, path string
		want        int
	}{
		{mech, "/api/v1/admin/drivers", 403},
		{mech, "/api/v1/admin/users", 403},
		{disp, "/api/v1/admin/users", 403},
		{disp, "/api/v1/admin/audit", 403},
		{disp, "/api/v1/admin/drivers", 200},
		{mech, "/api/v1/admin/buses", 200},
	} {
		if st, _ := callList(t, c.path, c.token); st != c.want {
			t.Errorf("GET %s: esperaba %d, llegó %d", c.path, c.want, st)
		}
	}

	// Driver and bus, as the SuperAdmin registers them.
	driverID := "E2E-" + tag
	st, d := call(t, "POST", "/api/v1/admin/drivers", sa, map[string]any{
		"id": driverID, "name": "Chofer " + tag, "email": "chofer-" + tag + "@rutacree.mx", "phone": "+52 618 000 0000", "password": "clave-de-prueba-1"})
	must(t, "crear chofer", st, 201, d)
	if d["canLogin"] != true || d["phone"] != "+52 618 000 0000" {
		t.Fatalf("chofer creado: %v", d)
	}
	number := "T" + tag
	st, b := call(t, "POST", "/api/v1/admin/buses", sa, map[string]any{"number": number, "plates": "dgo-" + tag})
	must(t, "crear unidad", st, 201, b)
	busID := b["id"]
	if b["status"] != "active" || b["plates"] != "DGO-"+tag {
		t.Fatalf("unidad creada: %v", b)
	}
	if st, _ := call(t, "POST", "/api/v1/admin/buses", sa, map[string]any{"number": number}); st != 409 {
		t.Fatalf("unidad repetida: %d", st)
	}

	// Scheduled shift by the dispatcher; the same driver can't get a second one that day.
	day := time.Now().AddDate(1, 0, rand.IntN(300)).Format(time.DateOnly)
	st, a := call(t, "POST", "/api/v1/admin/assignments", disp, map[string]any{"date": day, "driverId": driverID, "busId": busID, "routeId": "ruta-1"})
	must(t, "programar turno", st, 201, a)
	if st, _ := call(t, "POST", "/api/v1/admin/assignments", disp, map[string]any{"date": day, "driverId": driverID, "busId": busID, "routeId": "ruta-2"}); st != 409 {
		t.Fatalf("turno repetido: %d", st)
	}
	_, list := callList(t, "/api/v1/admin/assignments?from="+day+"&to="+day, disp)
	if len(list) != 1 || list[0]["driverId"] != driverID {
		t.Fatalf("turnos del día: %v", list)
	}

	// The mechanic sends the bus to the workshop: it can't be assigned, and a driver can't start on it.
	busPath := fmt.Sprintf("/api/v1/admin/buses/%v", busID)
	st, b = call(t, "PATCH", busPath, mech, map[string]any{"status": "workshop", "lastMaintenance": "2026-09-01"})
	must(t, "unidad al taller", st, 200, b)
	if b["status"] != "workshop" || b["lastMaintenance"] != "2026-09-01" {
		t.Fatalf("unidad en taller: %v", b)
	}
	next := time.Now().AddDate(1, 1, 0).Format(time.DateOnly)
	if st, _ := call(t, "POST", "/api/v1/admin/assignments", disp, map[string]any{"date": next, "driverId": driverID, "busId": busID, "routeId": "ruta-1"}); st != 400 {
		t.Fatalf("turno en unidad del taller: %d", st)
	}
	driverTok := loginAt(t, "/api/v1/auth/login", "chofer-"+tag+"@rutacree.mx", "clave-de-prueba-1")
	if st, _ := call(t, "POST", "/api/v1/driver/shifts", driverTok, map[string]any{"busNumber": number, "routeId": "ruta-1"}); st != 409 {
		t.Fatalf("iniciar ruta en unidad del taller: %d", st)
	}
	st, b = call(t, "PATCH", busPath, mech, map[string]any{"status": "ramp_broken"})
	must(t, "rampa dañada", st, 200, b)
	if b["rampOperational"] != false {
		t.Fatalf("rampa dañada debe apagar rampOperational: %v", b)
	}
	if st, _ := call(t, "PATCH", busPath, disp, map[string]any{"status": "active"}); st != 403 {
		t.Fatalf("el despachador no cambia la flotilla: %d", st)
	}
	call(t, "PATCH", busPath, sa, map[string]any{"status": "active"})

	// Rider report → in review with a resolution, cross-checked with the bus.
	st, rp := call(t, "POST", "/api/v1/reports", "", map[string]any{"type": "did_not_stop", "routeId": "ruta-1", "busId": busID, "comment": "prueba " + tag})
	must(t, "reporte", st, 201, rp)
	st, rp = call(t, "PATCH", fmt.Sprintf("/api/v1/admin/reports/%v", rp["id"]), sa, map[string]any{"status": "in_review", "resolution": "Se habló con el chofer."})
	must(t, "revisar reporte", st, 200, rp)
	if rp["status"] != "in_review" || rp["busNumber"] != number || rp["resolution"] != "Se habló con el chofer." {
		t.Fatalf("reporte en revisión: %v", rp)
	}
	if st, _ := call(t, "PATCH", fmt.Sprintf("/api/v1/admin/reports/%v", rp["id"]), disp, map[string]any{"status": "reviewed"}); st != 403 {
		t.Fatalf("el despachador no resuelve reportes: %d", st)
	}
	_, all := callList(t, "/api/v1/admin/reports?status=all&limit=500", disp)
	if !containsID(all, rp["id"]) {
		t.Fatal("status=all debe incluir el reporte en revisión")
	}

	// Audit trail names who did what.
	_, audit := callList(t, "/api/v1/admin/audit?limit=500", sa)
	joined := ""
	for _, e := range audit {
		joined += fmt.Sprint(e["actor"], ": ", e["action"], "\n")
	}
	for _, want := range []string{"mechanic_" + tag + ": editó la Unidad " + number, "dispatcher_" + tag + ": programó turno " + day, "creó la cuenta mechanic_" + tag} {
		if !strings.Contains(joined, want) {
			t.Errorf("auditoría sin %q", want)
		}
	}

	// Deactivating an account cuts its current session at once; nobody can remove themselves.
	st, u := call(t, "PATCH", "/api/v1/admin/users/"+accounts["mechanic-id"], sa, map[string]any{"active": false})
	must(t, "baja mecánico", st, 200, u)
	if st, _ := callList(t, "/api/v1/admin/buses", mech); st != 401 {
		t.Fatalf("token de cuenta dada de baja: %d", st)
	}
	if st, _ := call(t, "PATCH", fmt.Sprintf("/api/v1/admin/users/%v", me["id"]), sa, map[string]any{"active": false}); st != 400 {
		t.Fatalf("darse de baja a sí mismo: %d", st)
	}
	call(t, "PATCH", "/api/v1/admin/users/"+accounts["dispatcher-id"], sa, map[string]any{"active": false})
	call(t, "PATCH", "/api/v1/admin/drivers/"+driverID, sa, map[string]any{"active": false})
}

func containsID(list []map[string]any, id any) bool {
	for _, x := range list {
		if fmt.Sprint(x["id"]) == fmt.Sprint(id) {
			return true
		}
	}
	return false
}

func TestPanelStatic(t *testing.T) {
	resp, err := http.Get(base + "/panel/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 404 {
		t.Skip("el API no sirve el panel (PANEL_DIR vacío)")
	}
	if resp.StatusCode != 200 || !strings.Contains(string(body), "RutaCree") {
		t.Fatalf("/panel/: %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP del panel: %q", csp)
	}
	for _, p := range []string{"/panel/js/", "/panel/.git/config", "/panel/../go.mod"} {
		r, err := http.Get(base + p)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 404 && r.StatusCode != 400 {
			t.Errorf("%s: esperaba 404, llegó %d", p, r.StatusCode)
		}
	}
}

func TestPanelRidersAndMaintenance(t *testing.T) {
	pass := os.Getenv("E2E_ADMIN_PASSWORD")
	if pass == "" {
		t.Skip("define E2E_ADMIN_PASSWORD para probar el panel")
	}
	sa := adminLogin(t, envOr("E2E_ADMIN_EMAIL", "admin@rutacree.mx"), pass)
	tag := fmt.Sprintf("%05d", rand.IntN(100000))

	mechEmail := "mecanico-" + tag + "@rutacree.mx"
	st, u := call(t, "POST", "/api/v1/admin/users", sa, map[string]any{
		"name": "Mecánico " + tag, "username": "mec_" + tag, "email": mechEmail, "role": "mechanic", "password": "clave-de-prueba-1"})
	must(t, "crear mecánico", st, 201, u)
	mech := adminLogin(t, mechEmail, "clave-de-prueba-1")
	defer call(t, "PATCH", fmt.Sprintf("/api/v1/admin/users/%v", u["id"]), sa, map[string]any{"active": false})

	// Riders: only the SuperAdmin sees them.
	email := "pasajero-" + tag + "@rutacree.mx"
	st, reg := call(t, "POST", "/api/v1/riders", "", map[string]any{"email": email, "password": "clave-de-prueba-1", "name": "Pasajero " + tag})
	must(t, "registro pasajero", st, 201, reg)
	riderTok := reg["token"].(string)
	riderID := reg["rider"].(map[string]any)["id"]
	if st, _ := call(t, "GET", "/api/v1/admin/riders", mech, nil); st != 403 {
		t.Fatalf("el mecánico no ve pasajeros: %d", st)
	}
	st, found := call(t, "GET", "/api/v1/admin/riders?q=pasajero-"+tag, sa, nil)
	must(t, "buscar pasajero", st, 200, found)
	if list, _ := found["riders"].([]any); len(list) != 1 {
		t.Fatalf("búsqueda de pasajero: %v", found)
	}

	riderPath := fmt.Sprintf("/api/v1/admin/riders/%v", riderID)
	st, x := call(t, "PATCH", riderPath, sa, map[string]any{"active": false})
	must(t, "suspender", st, 200, x)
	if st, _ := call(t, "GET", "/api/v1/riders/me", riderTok, nil); st != 401 {
		t.Fatalf("sesión de pasajero suspendido: %d", st)
	}
	st, r := call(t, "POST", "/api/v1/auth/login", "", map[string]any{"email": email, "password": "clave-de-prueba-1"})
	for st == 429 && r["error"] != "locked" {
		time.Sleep(10 * time.Second)
		st, r = call(t, "POST", "/api/v1/auth/login", "", map[string]any{"email": email, "password": "clave-de-prueba-1"})
	}
	if st != 403 || r["error"] != "suspended" {
		t.Fatalf("login de pasajero suspendido: %d %v", st, r)
	}
	st, x = call(t, "PATCH", riderPath, sa, map[string]any{"active": true, "unlock": true})
	must(t, "reactivar", st, 200, x)
	if st, _ := call(t, "GET", "/api/v1/riders/me", riderTok, nil); st != 200 {
		t.Fatalf("sesión tras reactivar: %d", st)
	}
	if st, _ := call(t, "DELETE", riderPath, sa, nil); st != 204 {
		t.Fatalf("eliminar pasajero: %d", st)
	}
	if st, _ := call(t, "GET", "/api/v1/riders/me", riderTok, nil); st != 401 {
		t.Fatalf("sesión de pasajero eliminado: %d", st)
	}

	// Maintenance: the mechanic opens an order, the bus leaves service, closing it releases it.
	st, b := call(t, "POST", "/api/v1/admin/buses", sa, map[string]any{"number": "M" + tag})
	must(t, "crear unidad", st, 201, b)
	busPath := fmt.Sprintf("/api/v1/admin/buses/%v", b["id"])
	st, o := call(t, "POST", "/api/v1/admin/maintenance", mech, map[string]any{"busId": b["id"], "kind": "ramp", "description": "La rampa no baja"})
	must(t, "abrir orden", st, 201, o)
	if o["openedBy"] != "mec_"+tag || o["closedAt"] != nil {
		t.Fatalf("orden abierta: %v", o)
	}
	if st, _ := call(t, "POST", "/api/v1/admin/maintenance", mech, map[string]any{"busId": b["id"], "kind": "preventive", "description": "otra"}); st != 409 {
		t.Fatalf("segunda orden abierta: %d", st)
	}
	_, buses := callList(t, "/api/v1/admin/buses", mech)
	for _, x := range buses {
		if fmt.Sprint(x["id"]) == fmt.Sprint(b["id"]) && x["status"] != "workshop" {
			t.Fatalf("la unidad debe estar en taller: %v", x)
		}
	}
	orderPath := fmt.Sprintf("/api/v1/admin/maintenance/%v", o["id"])
	st, o = call(t, "PATCH", orderPath, mech, map[string]any{"notes": "Se cambió el pistón", "close": true, "date": "2026-09-20"})
	must(t, "cerrar orden", st, 200, o)
	if o["closedBy"] != "mec_"+tag || o["notes"] != "Se cambió el pistón" {
		t.Fatalf("orden cerrada: %v", o)
	}
	if st, _ := call(t, "PATCH", orderPath, mech, map[string]any{"close": true}); st != 409 {
		t.Fatalf("cerrar dos veces: %d", st)
	}
	st, after := call(t, "PATCH", busPath, mech, map[string]any{})
	must(t, "leer unidad", st, 200, after)
	if after["status"] != "active" || after["lastMaintenance"] != "2026-09-20" || after["rampOperational"] != true {
		t.Fatalf("unidad liberada: %v", after)
	}
	_, history := callList(t, fmt.Sprintf("/api/v1/admin/maintenance?open=false&busId=%v", b["id"]), sa)
	if len(history) != 1 {
		t.Fatalf("historial de la unidad: %v", history)
	}
}

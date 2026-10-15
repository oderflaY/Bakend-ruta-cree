# RutaCree — Backend

API en Go + MariaDB, en Docker, para la app RutaCree (`~/AndroidStudioProjects/RutaCree`).
Sustituye a `SimulatedFeed` y `SimulatedDispatch` de la app: posiciones de camiones en vivo,
turnos de choferes, solicitudes de rampa, incidentes y reportes de pasajeros, además de un API
para un panel de administración.

## Arranque

```sh
./scripts/generar-secretos.sh   # una vez: crea secrets/ con contraseñas aleatorias
cp .env.example .env            # ajusta ADMIN_EMAIL, puertos, orígenes
docker compose up -d --build
docker compose ps               # api y db deben verse "healthy"
```

Al primer arranque se aplican las migraciones y se importan las rutas (`seed/routes/*.geojson`)
y los choferes (`seed/drivers.json`), copiados de la app. **Los choferes se crean sin acceso** y no
pueden entrar hasta que se les asigne correo y contraseña:

```sh
docker compose exec api /api driver-login CH-014 chofer@ejemplo.mx   # pide la contraseña por la entrada estándar
```

o desde el panel: `PATCH /api/v1/admin/drivers/CH-014 {"email": "...", "password": "..."}`.

**Login único:** `POST /api/v1/auth/login {"email","password"}` responde `role: "driver"` (token para abrir
turno con `POST /api/v1/driver/shifts {"busNumber","routeId"}`) o `role: "rider"`. La app abre las
pantallas de chofer o de pasajero según esa respuesta; ya no hay PIN ni lista pública de choferes.

El administrador inicial es `ADMIN_EMAIL` con la contraseña de `secrets/admin_password`.

### Panel de control

El API sirve el panel (carpeta `Frontend admin`, que viaja en el paquete como `panel/`) en
`/panel/`; `/` redirige ahí. El administrador inicial es **SuperAdmin** y desde la pantalla
**Cuentas** crea las demás cuentas (SuperAdmin, Despachador, Mecánico) y **da de alta a los
choferes con su contraseña**, sin usar `driver-login`. Cada cambio queda en la auditoría
(`audit_log`). El contrato completo está en el README del panel.

En desarrollo, pon `PANEL_SRC=../Frontend admin` en `.env` para servir la carpeta del panel tal cual.

### Producción (HTTPS)

Pon `DOMAIN=api.tudominio.mx` y `API_BIND=127.0.0.1` en `.env`, apunta el DNS al servidor y:

```sh
docker compose --profile https up -d
```

Caddy obtiene y renueva el certificado solo. La app debe usar `wss://` y `https://` en producción.

## Chofer: GPS en el camión y avisos por WhatsApp

El teléfono del chofer **ya no manda su ubicación**: cada camión lleva su propio GPS que reporta a
Traccar (protocolo OsmAnd, puerto 5055) con el **número de unidad** como identificador. La app del chofer
solo sirve para:

- **Iniciar la ruta**: unidad, ruta y cuántos pasajeros suben (niños, adultos, mujeres). Se guarda la hora
  de inicio en `shifts.started_at` y los conteos en `shifts.children/adults/women`.
- **Fallas**: falla de rampa (`ramp_failure`: la unidad sigue en ruta, queda como *Rampa dañada* y los
  pasajeros reciben el aviso), falla del camión o choque (cierran el turno y avisan a los pasajeros).
- **Terminar la ruta**: se guarda la hora de fin.
- Recibir las solicitudes de rampa de los pasajeros.

Cada uno de esos avisos abre WhatsApp con el mensaje listo para el despacho; el número se configura en
`WHATSAPP_DESPACHO` (`.env`, solo dígitos con lada: `521618…`). Vacío, WhatsApp deja elegir el chat o grupo.
En el panel, **Turnos y Flotilla → Bitácora de viajes** muestra inicio, fin, duración y pasajeros de cada
ruta del día (`GET /api/v1/admin/shifts?from=…&to=…`, fechas RFC3339).

### Chatbot de WhatsApp (opcional)

Con la WhatsApp Cloud API de Meta, el chofer hace todo lo anterior **chateando con el número del
despacho**, sin la app: escribe «hola» y el bot le pregunta unidad, ruta, niños, adultos y mujeres, le
da un menú (falla de rampa, falla del camión, choque, terminar ruta) y le manda las solicitudes de rampa
con un botón «Enterado». Al chofer se le reconoce por el **teléfono de su cuenta** (panel → Cuentas →
Teléfono; cuentan los últimos 10 dígitos).

Variables en `.env` (el bot queda apagado si `WHATSAPP_PHONE_NUMBER_ID` está vacío):

| Variable | De dónde sale |
|---|---|
| `WHATSAPP_PHONE_NUMBER_ID` | Meta → app → WhatsApp → Configuración de la API → *Identificador del número de teléfono* |
| `WHATSAPP_TOKEN` | Token permanente de un usuario del sistema (permisos `whatsapp_business_messaging` y `whatsapp_business_management`) |
| `WHATSAPP_APP_SECRET` | Meta → app → Configuración → Básica → *Clave secreta de la app* |
| `WHATSAPP_VERIFY_TOKEN` | Lo inventas tú (16+ caracteres); se escribe igual en Meta |

En Meta → WhatsApp → Configuración → Webhook: URL `https://DOMINIO/api/whatsapp/webhook`, el mismo
token de verificación, y suscribir el campo **messages**. Las horas que escribe el bot usan `ZONA_HORARIA`
(por omisión `America/Mexico_City`).

## Traccar: GPS de los camiones

> Lo que sigue describe Traccar Client en el teléfono del chofer, que era el esquema anterior. Con el GPS
> del camión aplica igual: el dispositivo se configura con el número de unidad como identificador y
> `TRACCAR_PUBLIC_OSMAND_URL` como servidor. Solo cuentan sus posiciones mientras la ruta está iniciada.

El stack incluye Traccar 6.9 (servicio `traccar`, base de datos propia `traccar` en el mismo MariaDB).
El teléfono del chofer usa la app oficial **Traccar Client** (Android/iOS), que sigue enviando la
ubicación con la pantalla apagada; el API lee esas posiciones del WebSocket de Traccar y las publica en
`/api/socket` para los pasajeros. La app RutaCree del chofer se usa para el turno y las alertas de rampa.

```
Traccar Client ──OsmAnd :5055──▶ Traccar ──WebSocket──▶ API ──/api/socket──▶ pasajeros
RutaCree (chofer) ──login, turno, rampas──▶ API
```

- El identificador del dispositivo en Traccar es el **número de unidad** (ej. `245`). Al iniciar turno el
  API registra la unidad en Traccar y responde `positionSource: "traccar"`, `trackerDeviceId` y `trackerUrl`.
- Configura Traccar Client: *Identificador del dispositivo* = número de unidad, *URL del servidor* =
  `TRACCAR_PUBLIC_OSMAND_URL` (ej. `http://192.168.x.x:5055`), *Frecuencia* = 5 s, y activa el servicio.
- Solo cuentan las posiciones de unidades con un turno abierto; Traccar rechaza dispositivos no registrados
  y el API desactiva el auto-registro de usuarios en la web de Traccar.
- Mientras Traccar entrega posiciones, las que manda la app RutaCree se ignoran (evita que el pin salte);
  si Traccar Client deja de enviar 1 minuto, la app vuelve a ser la fuente. `GET /api/v1/driver/tracker`
  dice al chofer si su teléfono está reportando.
- Web de Traccar: `http://IP:8082`, usuario `TRACCAR_EMAIL`, contraseña en `secrets/traccar_password`
  (la cuenta de servicio del API, administradora). Ahí se ve el teléfono en el mapa y su historial.

Instalación existente (volumen de MariaDB creado antes de Traccar):

```sh
./scripts/generar-secretos.sh   # añade traccar_db_password, traccar_password y secrets/traccar.xml
./scripts/traccar-db.sh         # crea la base y el usuario de Traccar
docker compose up -d --build
```

## Cuentas de pasajero (opcionales)

La app funciona sin cuenta. Con cuenta, el perfil de accesibilidad y las paradas favoritas se guardan en
el servidor. Las solicitudes de rampa y los reportes siguen siendo anónimos (no se ligan a la cuenta).

| Método y ruta | Auth | Descripción |
|---|---|---|
| `POST /api/v1/riders` | — | `{"email","password","name"}` → `{"token","expiresAt","rider"}` (409 si el correo existe) |
| `GET /api/v1/riders/me` | pasajero | Perfil |
| `PUT /api/v1/riders/me` | pasajero | `{"name","userType","wheelchair","lowVision","reducedMobility","favorites":{"home","center","therapy"}}` |
| `DELETE /api/v1/riders/me` | pasajero | Borra la cuenta y su perfil |

Contraseñas de 8 a 128 caracteres, argon2id. El token (30 días) se guarda en el Keystore de Android /
Keychain de iOS, nunca en texto plano.

## Conectar la app

### 1. Mapa en vivo del pasajero — sin cambios de código

La app ya habla el protocolo del socket de Traccar, y este backend lo imita. En
`RutaCree/local.properties`:

```properties
TRACCAR_SOCKET_URL=ws://IP-DE-TU-PC:8080/api/socket
```

Con eso `AppConfig.isLive` pasa a `true` y `TraccarSocketFeed` recibe los camiones reales
(el teléfono y la PC deben estar en la misma red; en el emulador usa `10.0.2.2`).
Para HTTP sin TLS en desarrollo, Android exige permitir cleartext para esa IP
(`network_security_config`); en producción usa `wss://`.

### 2. Chofer — cambios pequeños en `DriverViewModel`

El chofer entra por el mismo login que los pasajeros (`POST /api/v1/auth/login` → `role: "driver"`).

1. **Iniciar turno** con el token del login:
   `POST /api/v1/driver/shifts` `{"busNumber":"245","routeId":"ruta-2","children":4,"adults":2,"women":1}` →
   `{"token":"…","shiftId":…,"busId":…,"expiresAt":"…","startedAt":"…","whatsapp":"521…"}`. Los conteos
   (0 a 99) son opcionales para apps viejas. Ese token de turno se guarda solo en memoria.
   Fallas: `POST /api/v1/driver/incidents {"type":"accident"|"breakdown"|"ramp_failure"}`. Cierre:
   `POST /api/v1/driver/shifts/end {"finalLocation"}` → `{"startedAt","endedAt"}`.
2. **Reportar posición**: `sendReport()` puede seguir igual, apuntando
   `TRACCAR_REPORT_URL=http://IP:8080/api/v1/driver/osmand` y añadiendo la cabecera
   `Authorization: Bearer <token>`. Los parámetros `alarm=accident|fault` y
   `status=terminal|workshop|depot` también funcionan (cierran el turno).
   El `id` debe ser el número de unidad del turno; otra unidad recibe 403.
3. **Solicitudes de rampa**: abre `ws://IP:8080/api/v1/driver/socket` con la misma cabecera.
   Llegan mensajes `{"rampRequests":[{"id","routeId","stopId","stopName","createdAt"}]}`
   (sustituye a `SimulatedDispatch.rampRequests`) y `{"rampAcks":[id]}` cuando otro chofer la atendió.
   Confirmar: `POST /api/v1/driver/ramp-requests/{id}/ack`.

### 3. Pasajero — rampa y reportes

- `TransitViewModel.requestRamp()`: `POST /api/v1/ramp-requests {"routeId","stopId"}` →
  `{"id","status","driversNotified"}`. Estado: `GET /api/v1/ramp-requests/{id}`
  (`pending` → `acknowledged`), para mostrar "el chofer ya lo vio".
- `ReportsScreen`: `POST /api/v1/reports`
  `{"type":"ramp_broken|did_not_stop|bus_full|stop_blocked","routeId"?,"busId"?,"comment"?}`.

### 4. Rutas desde el servidor (opcional)

`GET /api/v1/routes` → `{"routes":[FeatureCollection, …]}` en el mismo formato que
`files/routes/*.geojson`, así que `parseRouteGeoJson` sirve tal cual. Tiene `ETag`: manda
`If-None-Match` y recibirás `304` si no cambió.

## Referencia del API

Errores: `{"error":"código","message":"texto"}`. Cuerpos JSON con `Content-Type: application/json`;
campos desconocidos se rechazan.

| Método y ruta | Auth | Descripción |
|---|---|---|
| `GET /healthz`, `GET /readyz` | — | Vivo / base de datos accesible |
| `GET /api/socket` (WS) | — | Posiciones, unidades y alarmas (formato Traccar) |
| `GET /api/v1/routes`, `/routes/{id}` | — | Rutas en GeoJSON |
| `POST /api/v1/auth/login` | — | Login único: `role` `driver` o `rider` |
| `GET /api/v1/driver/me` | login de chofer | Datos del chofer |
| `POST /api/v1/ramp-requests` · `GET …/{id}` | — | Solicitar rampa / ver estado |
| `POST /api/v1/reports` | — | Reporte de pasajero |
| `POST /api/v1/driver/shifts` | login de chofer | Inicia turno, devuelve token de turno |
| `POST /api/v1/driver/positions` | chofer | `{"lat","lon","speedKmh"}` |
| `GET\|POST /api/v1/driver/osmand` | chofer | Compatible con OsmAnd/Traccar |
| `POST /api/v1/driver/incidents` | chofer | `{"type":"accident\|breakdown"}`, cierra turno |
| `POST /api/v1/driver/shifts/end` | chofer | `{"finalLocation":"terminal\|workshop\|depot"}` |
| `GET /api/v1/driver/socket` (WS) | chofer | Solicitudes de rampa de su ruta |
| `POST /api/v1/driver/ramp-requests/{id}/ack` | chofer | Atender solicitud |
| `POST /api/v1/admin/login` | — | `{"email","password"}` → token |
| `GET /api/v1/admin/overview` | admin | Números para el tablero |
| `GET /api/v1/admin/shifts/active` | admin | Turnos y posiciones en vivo |
| `GET\|POST /api/v1/admin/drivers`, `PATCH …/{id}` | admin | Choferes (nombre, activo, correo, contraseña) |
| `GET /api/v1/admin/buses`, `PATCH …/{id}` | admin | Unidades (`rampOperational`) |
| `GET /api/v1/admin/buses/{id}/track?from&to` | admin | Recorrido (máx. 24 h) |
| `PUT /api/v1/admin/routes`, `DELETE …/{id}` | admin | Subir GeoJSON / desactivar ruta |
| `GET /api/v1/admin/reports?status`, `PATCH …/{id}` | admin | Reportes |
| `GET /api/v1/admin/incidents?open=true`, `POST …/{id}/resolve` | admin | Incidentes |
| `GET /api/v1/admin/ramp-requests?status` | admin | Solicitudes de rampa |

Auth = `Authorization: Bearer <token>`. Los tokens nunca se aceptan en la URL (acabarían en logs).

## Seguridad

- **Secretos** como Docker secrets (archivos en `secrets/`, carpeta `700`), no variables de entorno ni `.env`.
- **MariaDB** sin puertos publicados, en una red interna sin salida a Internet.
- **Contenedor del API**: imagen distroless sin shell (~20 MB), usuario no root, sistema de archivos
  de solo lectura, sin capabilities, `no-new-privileges`, límites de memoria.
- **Contraseñas** con argon2id; tiempo constante aunque el usuario no exista (no revela cuentas).
- **Bloqueo** de 15 min tras 5 intentos fallidos, más límite de peticiones por IP en los logins.
- **JWT** HS256 con algoritmo fijado (rechaza `none` y otros), emisor y vencimiento obligatorios.
  El token del chofer está atado a su turno: al cerrarlo deja de servir aunque no haya vencido.
- **Autorización por recurso**: un chofer solo mueve su unidad y solo atiende rampas de su ruta.
- **Entrada validada**: tamaño máximo de cuerpo, JSON estricto, coordenadas y velocidades en rango,
  GeoJSON validado, SQL siempre parametrizado (`multiStatements` desactivado).
- **Límites** por IP (generosos para pasajeros detrás de CGNAT), máximo de WebSockets global y por
  IP, plazos de lectura/escritura contra clientes lentos, `X-Forwarded-For` solo desde Caddy.
- **Cabeceras** `nosniff`, `DENY`, CSP `default-src 'none'`, HSTS detrás de HTTPS; CORS y origen
  de WebSocket solo para `ALLOWED_ORIGINS`.
- **Privacidad**: solicitudes de rampa y reportes son anónimos; no se guarda IP del pasajero.

## Eficiencia

- Posiciones en memoria; el mapa en vivo nunca espera a la base de datos.
- Difusión agrupada cada 1 s, serializada una vez por lote (no una por cliente).
- Clientes lentos se desconectan en vez de frenar a los demás.
- Historial de posiciones escrito por lotes (INSERT multi-fila cada 2 s) y purgado por
  `POSITION_RETENTION_DAYS`, en trozos para no bloquear tablas.
- Catálogo de rutas en memoria con respuesta pre-serializada y `ETag`.

## Desarrollo

```sh
go test ./...                              # unitarias
echo 'clave-de-prueba' | docker compose exec -T api /api driver-login CH-014 chofer.e2e@rutacree.mx
E2E_ADMIN_PASSWORD="$(cat secrets/admin_password)" E2E_DRIVER_EMAIL=chofer.e2e@rutacree.mx \
  E2E_DRIVER_PASSWORD=clave-de-prueba go test -tags e2e ./e2e -v   # contra el stack
```

Al terminar, vuelve a asignar el acceso real del chofer.

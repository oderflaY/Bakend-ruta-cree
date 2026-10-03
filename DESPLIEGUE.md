# Subir RutaCree a un VPS

Probado desde cero: instalación limpia, chofer, turno, teléfono → Traccar → mapa, respaldo y restauración.

## 1. En tu PC: empaquetar y subir

```sh
cd "bakend ruta cree"
./scripts/empaquetar.sh                       # crea ../rutacree-backend.tar.gz (sin secretos)
scp ../rutacree-backend.tar.gz usuario@IP-DEL-VPS:~
```

## 2. En el VPS: instalar

VPS con Ubuntu/Debian, 2 vCPU y 4 GB de RAM.

```sh
curl -fsSL https://get.docker.com | sh        # solo si no tiene Docker
mkdir -p ~/rutacree && tar -xzf rutacree-backend.tar.gz -C ~/rutacree && cd ~/rutacree

./scripts/instalar-vps.sh IP-DEL-VPS          # pruebas, sin dominio (HTTP)
# o, con un dominio apuntando al VPS:
./scripts/instalar-vps.sh api.tudominio.mx    # HTTPS automático
```

Al terminar imprime la URL para la app, la URL para Traccar Client y qué puertos abrir.

Firewall (ejemplo con ufw):

```sh
ufw allow 22
ufw allow 8080 && ufw allow 5055    # modo IP
# ufw allow 80 && ufw allow 443     # modo dominio
ufw enable
```

Crea los choferes (la contraseña se escribe al pedirla):

```sh
docker compose exec api /api driver-login CH-001 chofer1@correo.mx "Nombre Apellido"
```

Respaldo diario a las 3:30 (`crontab -e`):

```
30 3 * * * cd ~/rutacree && ./scripts/respaldo.sh >> respaldos/respaldo.log 2>&1
```

Copia `respaldos/` fuera del VPS de vez en cuando. Para restaurar: `./scripts/restaurar.sh respaldos/ARCHIVO.sql.gz`.

## 3. En tu PC: compilar la app

```sh
cd AndroidStudioProjects/RutaCree
./scripts/compilar-app.sh http://IP-DEL-VPS:8080               # pruebas
./scripts/compilar-app.sh https://api.tudominio.mx pk.TOKEN    # producción (ver abajo)
```

El APK queda en `dist/`. Pásalo a los teléfonos o `adb install -r dist/…apk`.

- Sin token de Mapbox (`pk.…`, gratis en mapbox.com) se ve el mapa esquemático.
- Para el APK de producción firmado, una sola vez: `./scripts/crear-llave.sh` y **respalda `~/.rutacree`**
  (sin esa llave no puedes publicar actualizaciones).
- Con URL `http://` se genera el APK de depuración, porque Android solo permite HTTP en depuración.

## 4. Teléfono de cada chofer

1. Instala RutaCree y **Traccar Client** (Play Store).
2. Traccar Client: *Identificador del dispositivo* = número de la unidad (ej. `101`), *URL del servidor* =
   la que imprimió el instalador (`http://IP:5055` o `https://dominio/osmand`), *Frecuencia* = `5`.
   Activa el servicio.
3. Ajustes de Android → Traccar Client: ubicación **Permitir todo el tiempo** y batería **Sin restricciones**.
4. En RutaCree entra con su correo, escribe la misma unidad y la ruta, e **Iniciar ruta**.

## 5. Revisar que todo va bien

```sh
docker compose ps                                  # api, db y traccar "healthy"
docker compose logs api | grep "conectado a Traccar"
```

Web de Traccar (solo por túnel, no está expuesta): `ssh -L 8082:127.0.0.1:8082 usuario@VPS` y abre
`http://localhost:8082` con `traccar@rutacree.mx` y la contraseña de `secrets/traccar_password`.

**Si reconstruyes o reinicias servicios**, termina siempre con `docker compose up -d` (o
`docker compose --profile https up -d` con dominio): Docker no vuelve a levantar solo a Traccar si se
detuvo al reiniciar la base de datos.

## Pendiente antes del público

- Cargar las rutas, paradas y horarios **reales** (las de `seed/routes` son de ejemplo):
  `PUT /api/v1/admin/routes` con el GeoJSON de cada ruta.
- Aviso de privacidad (usa ubicación) y publicar en Play con `./gradlew :androidApp:bundleRelease`.

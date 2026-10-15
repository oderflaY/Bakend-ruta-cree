#!/usr/bin/env bash
# Sube el backend y el panel al VPS de producción, reconstruye solo el API (no toca .env, secretos,
# base de datos ni Traccar) y publica las rutas que se pasen.
#
#   ./scripts/desplegar.sh                                   # solo código y panel
#   ./scripts/desplegar.sh seed/routes/cas-{1,2,3}.geojson   # y además publica esas rutas
set -euo pipefail
cd "$(dirname "$0")/.."
vps="${VPS:-root@82.21.72.68}"
dir="${VPS_DIR:-/root/rutacree}"

(cd ../frontend-admin-react && npm run build >/dev/null)
./scripts/empaquetar.sh
scp -q ../rutacree-backend.tar.gz "$vps:/root/rutacree-backend.tar.gz"

# Rutas dentro del contenedor: el paquete trae seed/ y el Dockerfile lo copia a /seed.
routes=()
for f in "$@"; do routes+=("/seed/${f#seed/}"); done

ssh "$vps" bash -s -- "$dir" "${routes[@]}" <<'REMOTE'
set -euo pipefail
dir="$1"; shift
cd "$dir"
# El panel es un bind mount: se extrae encima (reemplazar la carpeta rompería el montaje).
# docker-compose.yml y Caddyfile de producción tienen cambios propios (TRUSTED_PROXIES, SEED_DIR vacío,
# el sitio de Fued): no se tocan.
tar -xzf /root/rutacree-backend.tar.gz -C "$dir" --exclude=./docker-compose.yml --exclude=./Caddyfile
docker compose --profile https up -d --build api
wait_healthy() {
	for _ in $(seq 1 60); do
		[ "$(docker compose ps api --format '{{.Health}}')" = healthy ] && return 0
		sleep 3
	done
	echo "El API no quedó sano:" >&2
	docker compose logs --tail 40 api >&2
	return 1
}
wait_healthy
if [ "$#" -gt 0 ]; then
	docker compose exec -T api /api publicar-ruta "$@"
	docker compose restart api >/dev/null
	wait_healthy
fi
docker compose ps --format 'table {{.Service}}\t{{.Status}}'
REMOTE
echo "Listo: https://82-21-72-68.sslip.io/panel/#/estadisticas"

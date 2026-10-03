#!/usr/bin/env bash
# Crea el paquete para subir al VPS, sin secretos, .env, respaldos ni binarios locales.
#
#   ./scripts/empaquetar.sh      →  ../rutacree-backend.tar.gz
#   scp ../rutacree-backend.tar.gz usuario@VPS:~
#   ssh usuario@VPS 'mkdir -p rutacree && tar -xzf rutacree-backend.tar.gz -C rutacree'
set -euo pipefail
cd "$(dirname "$0")/.."
out="../rutacree-backend.tar.gz"
panel="${PANEL_SRC:-../Frontend admin}"
if [ ! -f "$panel/index.html" ]; then
	echo "No encuentro el panel en '$panel' (usa PANEL_SRC=ruta)." >&2
	exit 1
fi

# El panel viaja dentro del paquete como ./panel (docker-compose lo monta en el API).
staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT
tar --exclude='./secrets' --exclude='./.env' --exclude='./respaldos' --exclude='./.git' --exclude='./api' \
	--exclude='./panel' -cf - . | tar -xf - -C "$staging"
mkdir "$staging/panel"
(cd "$panel" && tar --exclude='./.git' --exclude='*.md' -cf - .) | tar -xf - -C "$staging/panel"
tar -czf "$out" -C "$staging" .
echo "Paquete: $(cd .. && pwd)/rutacree-backend.tar.gz ($(du -h "$out" | cut -f1))"

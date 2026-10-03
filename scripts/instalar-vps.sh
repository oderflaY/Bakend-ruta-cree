#!/usr/bin/env bash
# Deja RutaCree corriendo en un VPS recién copiado.
#
#   ./scripts/instalar-vps.sh 203.0.113.10            # pruebas solo con IP (HTTP)
#   ./scripts/instalar-vps.sh api.rutacree.mx         # producción con dominio (HTTPS automático)
#
# Requisitos: Docker con el plugin compose. Se puede volver a correr: conserva secretos y datos.
set -euo pipefail
cd "$(dirname "$0")/.."

host="${1:-}"
admin_email="${2:-admin@rutacree.mx}"
if [ -z "$host" ]; then
	echo "uso: $0 <IP-o-dominio> [correo-del-administrador]" >&2
	exit 1
fi
if ! docker compose version >/dev/null 2>&1; then
	echo "Falta Docker. En Ubuntu/Debian:  curl -fsSL https://get.docker.com | sh" >&2
	exit 1
fi

# Un dominio lleva letras; una IPv4 solo dígitos y puntos.
if [[ "$host" =~ ^[0-9.]+$ ]]; then
	mode=ip
else
	mode=https
fi

./scripts/generar-secretos.sh >/dev/null

set_env() { # clave valor: reemplaza o agrega en .env
	if grep -q "^$1=" .env 2>/dev/null; then
		sed -i "s|^$1=.*|$1=$2|" .env
	else
		echo "$1=$2" >>.env
	fi
}
[ -f .env ] || cp .env.example .env
set_env ADMIN_EMAIL "$admin_email"
set_env TRACCAR_EMAIL "traccar@rutacree.mx"
set_env TRACCAR_WEB_BIND "127.0.0.1"
if [ "$mode" = https ]; then
	set_env DOMAIN "$host"
	set_env API_BIND "127.0.0.1"
	set_env TRACCAR_BIND "127.0.0.1"
	set_env TRACCAR_PUBLIC_OSMAND_URL "https://$host/osmand"
	api_url="https://$host"
	profile=(--profile https)
else
	sed -i "/^DOMAIN=/d" .env
	set_env API_BIND "0.0.0.0"
	set_env TRACCAR_BIND "0.0.0.0"
	set_env TRACCAR_PUBLIC_OSMAND_URL "http://$host:5055"
	api_url="http://$host:8080"
	profile=()
fi

docker compose "${profile[@]}" up -d --build

echo "Esperando a que todo esté sano…"
for _ in $(seq 1 90); do
	status="$(docker compose ps --format '{{.Service}}={{.Health}}' | tr '\n' ' ')"
	if [[ "$status" == *"api=healthy"* && "$status" == *"traccar=healthy"* && "$status" == *"db=healthy"* ]]; then
		break
	fi
	sleep 4
done
docker compose "${profile[@]}" ps --format 'table {{.Service}}\t{{.Status}}'

cat <<EOF

==================================================================
 RutaCree está corriendo ($mode)
------------------------------------------------------------------
 App (compilarla con esto):   API_BASE_URL=$api_url
 Traccar Client (choferes):   URL del servidor = $(grep ^TRACCAR_PUBLIC_OSMAND_URL= .env | cut -d= -f2-)
                              Frecuencia = 5 s, identificador = número de unidad
 Crear chofer:
   docker compose exec api /api driver-login CH-001 correo@ejemplo.mx "Nombre Apellido"
 Web de Traccar (solo por túnel SSH desde tu PC):
   ssh -L 8082:127.0.0.1:8082 usuario@$host   y abre http://localhost:8082
   usuario traccar@rutacree.mx, contraseña en secrets/traccar_password
 Respaldo diario (crontab -e):
   30 3 * * * cd $(pwd) && ./scripts/respaldo.sh >> respaldos/respaldo.log 2>&1
------------------------------------------------------------------
 Firewall: abre solo 22 y $([ "$mode" = https ] && echo "80, 443" || echo "8080, 5055")
==================================================================
EOF

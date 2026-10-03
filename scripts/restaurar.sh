#!/usr/bin/env bash
# Restaura un respaldo hecho con respaldo.sh. REEMPLAZA los datos actuales de RutaCree y Traccar.
#
#   ./scripts/restaurar.sh respaldos/rutacree-20260927-0330.sql.gz
set -euo pipefail
cd "$(dirname "$0")/.."
file="${1:-}"
if [ ! -f "$file" ]; then
	echo "uso: $0 <respaldos/rutacree-FECHA.sql.gz>" >&2
	exit 1
fi
read -r -p "Esto reemplaza TODOS los datos actuales con $file. Escribe RESTAURAR para seguir: " answer
[ "$answer" = "RESTAURAR" ] || { echo "Cancelado."; exit 1; }

# Detener a quienes escriben mientras se restaura.
docker compose stop api traccar
gunzip -c "$file" | docker compose exec -T -e MYSQL_PWD="$(cat secrets/db_root_password)" db mariadb -uroot
docker compose start traccar api
echo "Restaurado. Revisa: docker compose ps"

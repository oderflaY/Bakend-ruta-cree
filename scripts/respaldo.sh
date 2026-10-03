#!/usr/bin/env bash
# Respaldo de las bases de RutaCree y Traccar (y de los secretos). Guarda los últimos 14 días en
# ./respaldos. Copia esa carpeta fuera del servidor: un respaldo en el mismo disco no protege de perderlo.
#
#   ./scripts/respaldo.sh
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077
mkdir -p respaldos
stamp="$(date +%Y%m%d-%H%M)"
out="respaldos/rutacree-$stamp.sql.gz"

# La contraseña va por variable de entorno del proceso, no en la línea de comandos (ps / historial).
docker compose exec -T -e MYSQL_PWD="$(cat secrets/db_root_password)" db \
	mariadb-dump -uroot --single-transaction --routines --triggers --databases rutacree traccar |
	gzip >"$out.tmp"
mv "$out.tmp" "$out"

# Sin los secretos no se puede entrar a nada tras restaurar en otro servidor.
tar -czf "respaldos/secretos-$stamp.tar.gz" secrets .env

find respaldos -name 'rutacree-*.sql.gz' -mtime +14 -delete
find respaldos -name 'secretos-*.tar.gz' -mtime +14 -delete
echo "$(date -Is) respaldo listo: $out ($(du -h "$out" | cut -f1))"

#!/usr/bin/env sh
# For a MariaDB volume created before Traccar was added: creates Traccar's database and user.
set -eu
cd "$(dirname "$0")/.."
docker compose exec -T db sh /docker-entrypoint-initdb.d/10-traccar.sh

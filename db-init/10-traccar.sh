#!/bin/sh
# Creates Traccar's own database and user inside the same MariaDB. MariaDB runs this automatically
# on a fresh volume; for an existing one run scripts/traccar-db.sh. Safe to run more than once.
set -eu
pw="$(cat /run/secrets/traccar_db_password)"
root="$(cat /run/secrets/db_root_password)"
mariadb -uroot -p"$root" <<SQL
CREATE DATABASE IF NOT EXISTS traccar CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'traccar'@'%' IDENTIFIED BY '$pw';
ALTER USER 'traccar'@'%' IDENTIFIED BY '$pw';
GRANT ALL PRIVILEGES ON traccar.* TO 'traccar'@'%';
FLUSH PRIVILEGES;
SQL
echo "base de datos de Traccar lista"

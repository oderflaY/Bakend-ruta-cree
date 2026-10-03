#!/usr/bin/env sh
# Creates the random secrets docker-compose mounts from ./secrets. Existing files are kept,
# so re-running it never rotates a password by accident.
set -eu
cd "$(dirname "$0")/.."
umask 077
mkdir -p secrets

gen() { # $1 = file, $2 = bytes of entropy
	if [ -s "secrets/$1" ]; then
		echo "secrets/$1 ya existe, se conserva"
	else
		# base64 without / + = so it's safe in DSNs and shells
		head -c "$2" /dev/urandom | base64 | tr -d '/+=\n' | head -c "$(($2 * 4 / 3))" > "secrets/$1"
		echo "secrets/$1 creado"
	fi
}

gen db_password 32
gen db_root_password 32
gen jwt_secret 48
gen admin_password 18
gen traccar_db_password 32
gen traccar_password 18

# Traccar reads its settings (including the database password) from one XML file.
if [ -s secrets/traccar.xml ]; then
	echo "secrets/traccar.xml ya existe, se conserva"
else
	cat > secrets/traccar.xml <<XML
<?xml version='1.0' encoding='UTF-8'?>
<!DOCTYPE properties SYSTEM 'http://java.sun.com/dtd/properties.dtd'>
<properties>
    <entry key='database.driver'>org.mariadb.jdbc.Driver</entry>
    <entry key='database.url'>jdbc:mariadb://db:3306/traccar</entry>
    <entry key='database.user'>traccar</entry>
    <entry key='database.password'>$(cat secrets/traccar_db_password)</entry>
    <!-- Solo OsmAnd (el protocolo de Traccar Client): no abrir un puerto por cada protocolo. -->
    <entry key='protocols.enable'>osmand</entry>
    <entry key='osmand.port'>5055</entry>
    <entry key='web.port'>8082</entry>
</properties>
XML
	echo "secrets/traccar.xml creado"
fi

# The folder is private to you on the host; the files must be readable inside the containers,
# where MariaDB (uid 999) and the API (uid 65532) read them through bind mounts.
chmod 700 secrets
chmod 644 secrets/*
echo
echo "Contraseña inicial del administrador: $(cat secrets/admin_password)"
echo "(guárdala en tu gestor de contraseñas; está en secrets/admin_password)"
echo "Contraseña de Traccar (web en :8082, usuario TRACCAR_EMAIL): $(cat secrets/traccar_password)"

# Prueba de estrés

`go run ./estres` simula el uso real de la app y sube de pasajeros por escalones hasta que el
servidor deja de cumplir los objetivos. El último escalón que pasa es la capacidad.

**Qué simula**

- **Choferes** (`-buses`, 30 por defecto): alta en el panel como `LT-001…`, inicio de sesión,
  turno en la unidad `LT001…`, posición cada 3 s recorriendo su ruta y socket de chofer que
  marca como vistas las solicitudes de rampa. Al terminar cierra los turnos.
- **Pasajeros**: piden las rutas (la mitad con ETag, como quien ya abrió la app), abren el mapa en
  vivo (`/api/socket`), cada uno con su propia IP (`-spoof`) y conexiones. Con `-ramp-rate` piden
  rampa y consultan su estado cada 3 s. Cada sesión dura en promedio `-session` (5 min) y otro
  pasajero toma su lugar. Si el socket falla reintentan con espera de 2 s → 30 s, como la app.

**Objetivos por escalón** (ajustables): errores < 1 % (HTTP y conexiones de socket), HTTP p95
< 1 s, del reporte GPS a la pantalla del pasajero p95 < 2.5 s, menos de 1 % de sockets caídos.

## Réplica con el tamaño del VPS

No se estresa producción: los límites por IP (600 req/min, 200 sockets) harían que solo se mida
el rate limiter, y los pasajeros reales sufrirían la prueba. La réplica corre el mismo stack
(API, MariaDB, Traccar, Caddy con TLS) dentro de un cgroup con el presupuesto del VPS:

```sh
# Una vez: cgroup del tamaño del VPS. Un vCPU del VPS (Xeon E5-2680 v2) rinde ~1/3 de un
# núcleo E del i5-12450HX, así que 2 vCPU ≈ 70 % de un núcleo; memoria 1.7 GB.
sudo tee /etc/systemd/system/rutacree-estres.slice <<'EOF'
[Slice]
AllowedCPUs=8,9
CPUQuota=70%
MemoryMax=1700M
EOF
sudo systemctl daemon-reload && sudo systemctl start rutacree-estres.slice

docker compose -p rutacree-estres -f docker-compose.yml -f estres/compose.yml --profile https up -d --build

ulimit -n 65535
taskset -c 0-7 go run ./estres -url https://localhost:8443 -insecure -spoof \
  -steps 250,500,1000,1500,2000,3000,4000,5000 -out resultados.json \
  -stats-cmd "docker stats --no-stream --format '{{.Name}} {{.CPUPerc}} {{.MemUsage}}' \
    rutacree-estres-api-1 rutacree-estres-caddy-1 rutacree-estres-db-1 rutacree-estres-traccar-1"

# Al terminar
docker compose -p rutacree-estres -f docker-compose.yml -f estres/compose.yml --profile https down -v
```

En `docker stats` el 100 % es un núcleo de la laptop; con la cuota de 70 %, llegar a ~70 % sumando
contenedores equivale a tener los 2 vCPU del VPS al tope.

Para recalibrar con otra máquina compara un mismo binario de Go (JSON + ECDSA, un hilo) en la
laptop (`taskset -c 8`) y en el VPS, y ajusta `CPUQuota`.

## Contra producción

Solo de forma ligera y sin escribir datos: `-buses 0 -ramp-rate 0 -steps 50,100,150` (debajo del
límite de 200 sockets por IP). Sirve para comparar latencias de red reales con la réplica.

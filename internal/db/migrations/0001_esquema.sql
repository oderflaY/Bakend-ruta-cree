-- Rutas: el GeoJSON se guarda tal cual lo lee la app (parseRouteGeoJson) y se sirve sin reprocesar.
CREATE TABLE IF NOT EXISTS routes (
    id         VARCHAR(64)  NOT NULL PRIMARY KEY,
    name       VARCHAR(120) NOT NULL,
    color      CHAR(7)      NOT NULL DEFAULT '#8ECAE6',
    geojson    LONGTEXT     NOT NULL CHECK (JSON_VALID(geojson)),
    active     BOOLEAN      NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

-- Paradas extraídas del GeoJSON, para validar solicitudes de rampa y reportes.
CREATE TABLE IF NOT EXISTS stops (
    id         VARCHAR(64)  NOT NULL PRIMARY KEY,
    route_id   VARCHAR(64)  NOT NULL,
    name       VARCHAR(120) NOT NULL,
    lat        DECIMAL(9,6) NOT NULL,
    lon        DECIMAL(9,6) NOT NULL,
    `accessible` BOOLEAN      NOT NULL DEFAULT FALSE,
    seq        INT          NOT NULL,
    CONSTRAINT fk_stops_route FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
    INDEX idx_stops_route (route_id, seq)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS admins (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email         VARCHAR(190)    NOT NULL UNIQUE,
    password_hash VARCHAR(255)    NOT NULL,
    failed_logins INT             NOT NULL DEFAULT 0,
    locked_until  TIMESTAMP(3)    NULL,
    created_at    TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

-- Choferes: sin pin_hash no pueden iniciar turno (un admin se lo asigna).
CREATE TABLE IF NOT EXISTS drivers (
    id            VARCHAR(16)  NOT NULL PRIMARY KEY,
    name          VARCHAR(120) NOT NULL,
    pin_hash      VARCHAR(255) NULL,
    active        BOOLEAN      NOT NULL DEFAULT TRUE,
    failed_logins INT          NOT NULL DEFAULT 0,
    locked_until  TIMESTAMP(3) NULL,
    created_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

-- Unidades. El id numérico es el deviceId que ve la app (como en Traccar).
CREATE TABLE IF NOT EXISTS buses (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    number           VARCHAR(8)      NOT NULL UNIQUE,
    ramp_operational BOOLEAN         NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS shifts (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    driver_id      VARCHAR(16)     NOT NULL,
    bus_id         BIGINT UNSIGNED NOT NULL,
    route_id       VARCHAR(64)     NOT NULL,
    started_at     TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    ended_at       TIMESTAMP(3)    NULL,
    end_reason     ENUM('completed','emergency','expired','replaced') NULL,
    final_location ENUM('terminal','workshop','depot') NULL,
    CONSTRAINT fk_shifts_driver FOREIGN KEY (driver_id) REFERENCES drivers(id),
    CONSTRAINT fk_shifts_bus    FOREIGN KEY (bus_id)    REFERENCES buses(id),
    CONSTRAINT fk_shifts_route  FOREIGN KEY (route_id)  REFERENCES routes(id),
    INDEX idx_shifts_open (ended_at),
    INDEX idx_shifts_driver (driver_id, started_at)
) ENGINE=InnoDB;

-- Historial de posiciones (se inserta por lotes y se purga según POSITION_RETENTION_DAYS).
CREATE TABLE IF NOT EXISTS positions (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    shift_id    BIGINT UNSIGNED NOT NULL,
    bus_id      BIGINT UNSIGNED NOT NULL,
    lat         DECIMAL(9,6)    NOT NULL,
    lon         DECIMAL(9,6)    NOT NULL,
    speed_kmh   DECIMAL(5,1)    NOT NULL,
    recorded_at TIMESTAMP(3)    NOT NULL,
    INDEX idx_positions_bus_time (bus_id, recorded_at),
    INDEX idx_positions_time (recorded_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS incidents (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    shift_id    BIGINT UNSIGNED NOT NULL,
    bus_id      BIGINT UNSIGNED NOT NULL,
    route_id    VARCHAR(64)     NOT NULL,
    type        ENUM('accident','breakdown') NOT NULL,
    lat         DECIMAL(9,6)    NULL,
    lon         DECIMAL(9,6)    NULL,
    created_at  TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    resolved_at TIMESTAMP(3)    NULL,
    CONSTRAINT fk_incidents_shift FOREIGN KEY (shift_id) REFERENCES shifts(id),
    INDEX idx_incidents_open (resolved_at, created_at)
) ENGINE=InnoDB;

-- Solicitudes de rampa de pasajeros. Anónimas: no se guarda IP ni identificador del pasajero.
CREATE TABLE IF NOT EXISTS ramp_requests (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    route_id        VARCHAR(64)     NOT NULL,
    stop_id         VARCHAR(64)     NOT NULL,
    status          ENUM('pending','acknowledged','expired') NOT NULL DEFAULT 'pending',
    ack_shift_id    BIGINT UNSIGNED NULL,
    created_at      TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    acknowledged_at TIMESTAMP(3)    NULL,
    CONSTRAINT fk_ramp_stop FOREIGN KEY (stop_id) REFERENCES stops(id) ON DELETE CASCADE,
    INDEX idx_ramp_status (status, created_at)
) ENGINE=InnoDB;

-- Reportes rápidos de pasajeros (pantalla Reportes). También anónimos.
CREATE TABLE IF NOT EXISTS reports (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    type       ENUM('ramp_broken','did_not_stop','bus_full','stop_blocked') NOT NULL,
    route_id   VARCHAR(64)     NULL,
    bus_id     BIGINT UNSIGNED NULL,
    comment    VARCHAR(500)    NOT NULL DEFAULT '',
    status     ENUM('open','reviewed','dismissed') NOT NULL DEFAULT 'open',
    created_at TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_reports_status (status, created_at)
) ENGINE=InnoDB;

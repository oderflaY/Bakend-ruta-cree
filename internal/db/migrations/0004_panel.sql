-- Panel de control (Frontend admin): cuentas con rol, estado de la flotilla, turnos programados,
-- seguimiento de reportes e incidentes, y registro de auditoría.

-- Cuentas del panel. El administrador que ya existía queda como SuperAdmin.
ALTER TABLE admins ADD COLUMN IF NOT EXISTS name VARCHAR(120) NOT NULL DEFAULT '' AFTER id;
ALTER TABLE admins ADD COLUMN IF NOT EXISTS username VARCHAR(40) NULL AFTER name;
ALTER TABLE admins ADD COLUMN IF NOT EXISTS role ENUM('superadmin','dispatcher','mechanic') NOT NULL DEFAULT 'superadmin' AFTER email;
ALTER TABLE admins ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE AFTER role;
UPDATE admins SET username = LEFT(SUBSTRING_INDEX(email, '@', 1), 40) WHERE username IS NULL;
UPDATE admins SET name = username WHERE name = '';
CREATE UNIQUE INDEX IF NOT EXISTS uq_admins_username ON admins (username);

-- Teléfono del chofer: el despachador le llama desde el mapa.
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS phone VARCHAR(20) NOT NULL DEFAULT '' AFTER email;

-- Flotilla. ramp_operational se mantiene (lo usan la app y el feed) y va en sincronía con status.
ALTER TABLE buses ADD COLUMN IF NOT EXISTS plates VARCHAR(16) NOT NULL DEFAULT '' AFTER number;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS status ENUM('active','workshop','ramp_broken') NOT NULL DEFAULT 'active' AFTER plates;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS last_maintenance DATE NULL AFTER status;
UPDATE buses SET status = 'ramp_broken' WHERE ramp_operational = FALSE;

-- Un turno abierto se cierra con 'withdrawn' cuando la unidad entra al taller.
ALTER TABLE shifts MODIFY end_reason ENUM('completed','emergency','expired','replaced','withdrawn') NULL;

-- Turnos programados: quién maneja qué unidad y en qué ruta cada día.
CREATE TABLE IF NOT EXISTS shift_assignments (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    day        DATE            NOT NULL,
    driver_id  VARCHAR(16)     NOT NULL,
    bus_id     BIGINT UNSIGNED NOT NULL,
    route_id   VARCHAR(64)     NOT NULL,
    created_by BIGINT UNSIGNED NULL,
    created_at TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_asg_driver FOREIGN KEY (driver_id) REFERENCES drivers(id),
    CONSTRAINT fk_asg_bus    FOREIGN KEY (bus_id)    REFERENCES buses(id),
    CONSTRAINT fk_asg_route  FOREIGN KEY (route_id)  REFERENCES routes(id),
    UNIQUE KEY uq_asg_driver (day, driver_id),
    UNIQUE KEY uq_asg_bus (day, bus_id)
) ENGINE=InnoDB;

-- Seguimiento: columna "En revisión" del tablero y la resolución que anota el administrador.
ALTER TABLE reports MODIFY status ENUM('open','in_review','reviewed','dismissed') NOT NULL DEFAULT 'open';
ALTER TABLE reports ADD COLUMN IF NOT EXISTS resolution VARCHAR(1000) NOT NULL DEFAULT '';
ALTER TABLE incidents ADD COLUMN IF NOT EXISTS status ENUM('open','in_review','resolved') NOT NULL DEFAULT 'open' AFTER type;
ALTER TABLE incidents ADD COLUMN IF NOT EXISTS resolution VARCHAR(1000) NOT NULL DEFAULT '';
UPDATE incidents SET status = 'resolved' WHERE resolved_at IS NOT NULL;

-- Auditoría: cada cambio hecho desde el panel, con su autor. actor se copia para que el registro
-- siga siendo legible aunque la cuenta cambie de nombre.
CREATE TABLE IF NOT EXISTS audit_log (
    id       BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    at       TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    admin_id BIGINT UNSIGNED NULL,
    actor    VARCHAR(40)     NOT NULL,
    action   VARCHAR(500)    NOT NULL,
    INDEX idx_audit_at (at)
) ENGINE=InnoDB;

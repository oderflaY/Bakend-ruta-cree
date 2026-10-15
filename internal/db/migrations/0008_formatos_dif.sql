-- Formatos del Departamento de Ruta Azul (DIF Estatal): bitácora semanal de vueltas y usuarios,
-- solicitud y autorización del servicio (orden de servicio) y reporte de vehículo.

-- Usuarios por vuelta, como en "Número de vueltas y usuarios por día". women ya existía (mujeres);
-- children y adults quedan solo para el historial.
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS disability SMALLINT UNSIGNED NULL AFTER women;
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS elderly    SMALLINT UNSIGNED NULL AFTER disability;
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS pregnant   SMALLINT UNSIGNED NULL AFTER elderly;
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS companions SMALLINT UNSIGNED NULL AFTER pregnant;
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS men        SMALLINT UNSIGNED NULL AFTER companions;

-- Datos de la unidad que piden los formatos (MERCEDES No. ECONÓMICO 25-1451 PLACAS 13-EZC-C).
ALTER TABLE buses MODIFY number VARCHAR(16) NOT NULL;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS brand VARCHAR(40) NOT NULL DEFAULT '' AFTER plates;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS model VARCHAR(40) NOT NULL DEFAULT '' AFTER brand;

-- Orden de servicio: los campos de la solicitud y sus conceptos (descripción, unidad, cantidad).
ALTER TABLE maintenance_orders ADD COLUMN IF NOT EXISTS km         INT UNSIGNED  NULL AFTER notes;
ALTER TABLE maintenance_orders ADD COLUMN IF NOT EXISTS partida    VARCHAR(60)   NOT NULL DEFAULT '' AFTER km;
ALTER TABLE maintenance_orders ADD COLUMN IF NOT EXISTS recurso    VARCHAR(60)   NOT NULL DEFAULT '' AFTER partida;
ALTER TABLE maintenance_orders ADD COLUMN IF NOT EXISTS execution  VARCHAR(60)   NOT NULL DEFAULT 'MANTENIMIENTO PROVEEDOR' AFTER recurso;
ALTER TABLE maintenance_orders ADD COLUMN IF NOT EXISTS items      LONGTEXT      NOT NULL DEFAULT '[]' CHECK (JSON_VALID(items)) AFTER execution;

-- Reporte de vehículo: fecha, unidad y observaciones, con quién lo levantó.
CREATE TABLE IF NOT EXISTS vehicle_reports (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    bus_id       BIGINT UNSIGNED NOT NULL,
    day          DATE            NOT NULL,
    observations VARCHAR(2000)   NOT NULL,
    created_by   BIGINT UNSIGNED NULL,
    created_at   TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_vreport_bus FOREIGN KEY (bus_id) REFERENCES buses(id),
    INDEX idx_vreport_day (day)
) ENGINE=InnoDB;

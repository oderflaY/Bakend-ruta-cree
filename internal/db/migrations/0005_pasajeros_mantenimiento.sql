-- Panel: suspender cuentas de pasajero y órdenes de mantenimiento de la flotilla.

-- Un pasajero suspendido no puede entrar y su sesión abierta deja de funcionar.
ALTER TABLE riders ADD COLUMN IF NOT EXISTS active BOOLEAN NOT NULL DEFAULT TRUE AFTER password_hash;

-- Orden de taller: abrirla manda la unidad al taller; cerrarla la libera y registra el mantenimiento.
CREATE TABLE IF NOT EXISTS maintenance_orders (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    bus_id      BIGINT UNSIGNED NOT NULL,
    kind        ENUM('preventive','corrective','ramp') NOT NULL,
    description VARCHAR(500)    NOT NULL,
    notes       VARCHAR(2000)   NOT NULL DEFAULT '',
    opened_by   BIGINT UNSIGNED NULL,
    opened_at   TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    closed_by   BIGINT UNSIGNED NULL,
    closed_at   TIMESTAMP(3)    NULL,
    CONSTRAINT fk_maint_bus FOREIGN KEY (bus_id) REFERENCES buses(id),
    INDEX idx_maint_open (closed_at, opened_at),
    INDEX idx_maint_bus (bus_id, opened_at)
) ENGINE=InnoDB;

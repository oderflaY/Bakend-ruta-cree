-- Lo que se escribe a mano en los formatos desde el panel. Cada formato (bitácora de una semana,
-- orden de servicio, reporte de vehículo) guarda solo las celdas que difieren del valor automático:
-- {"HOJA": {"D8": "07:30"}}.
CREATE TABLE IF NOT EXISTS form_values (
    kind       ENUM('bitacora','orden','vehiculo') NOT NULL,
    ref        VARCHAR(32)     NOT NULL,
    data       LONGTEXT        NOT NULL DEFAULT '{}' CHECK (JSON_VALID(data)),
    updated_by BIGINT UNSIGNED NULL,
    updated_at TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (kind, ref)
) ENGINE=InnoDB;

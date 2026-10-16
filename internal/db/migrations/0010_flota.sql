-- Parque vehicular del DIF (Excel "PARQUE VEHICULAR"): por unidad, además del No. económico y placas,
-- la serie (VIN), el año, el número de camión pintado en la unidad y el centro al que pertenece.
ALTER TABLE buses ADD COLUMN IF NOT EXISTS serial  VARCHAR(20) NOT NULL DEFAULT '' AFTER model;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS year    SMALLINT UNSIGNED NULL AFTER serial;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS unit_no VARCHAR(8)  NOT NULL DEFAULT '' AFTER year;
ALTER TABLE buses ADD COLUMN IF NOT EXISTS centro  VARCHAR(60) NOT NULL DEFAULT '' AFTER unit_no;

-- "retired": la unidad ya no está en el parque (dada de baja). No se borra: turnos y reportes la citan.
ALTER TABLE buses MODIFY status ENUM('active','workshop','ramp_broken','retired') NOT NULL DEFAULT 'active';

-- El chofer ya no manda su ubicación (el camión lleva su propio GPS en Traccar) y reporta por
-- WhatsApp. Al iniciar la ruta dice cuántos pasajeros lleva; el inicio y el fin quedan en el turno.
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS children SMALLINT UNSIGNED NULL AFTER route_id;
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS adults SMALLINT UNSIGNED NULL AFTER children;
ALTER TABLE shifts ADD COLUMN IF NOT EXISTS women SMALLINT UNSIGNED NULL AFTER adults;

-- Falla de rampa reportada por el chofer: la unidad sigue en ruta, pero sin rampa.
ALTER TABLE incidents MODIFY type ENUM('accident','breakdown','ramp_failure') NOT NULL;

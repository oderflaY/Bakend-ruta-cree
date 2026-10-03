-- Login unificado: choferes y pasajeros entran con correo y contraseña en la misma pantalla y el
-- servidor decide el rol. El PIN del chofer se retira (lo sustituye la contraseña).
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS email VARCHAR(190) NULL AFTER name;
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS password_hash VARCHAR(255) NULL AFTER email;
CREATE UNIQUE INDEX IF NOT EXISTS uq_drivers_email ON drivers (email);
ALTER TABLE drivers DROP COLUMN IF EXISTS pin_hash;

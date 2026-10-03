-- Cuentas opcionales de pasajero: perfil de accesibilidad y paradas favoritas sincronizados.
-- Las solicitudes de rampa y los reportes siguen siendo anónimos (no se ligan a la cuenta).
CREATE TABLE IF NOT EXISTS riders (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    email            VARCHAR(190)    NOT NULL UNIQUE,
    name             VARCHAR(120)    NOT NULL,
    password_hash    VARCHAR(255)    NOT NULL,
    user_type        ENUM('rider','companion') NOT NULL DEFAULT 'rider',
    wheelchair       BOOLEAN         NOT NULL DEFAULT FALSE,
    low_vision       BOOLEAN         NOT NULL DEFAULT FALSE,
    reduced_mobility BOOLEAN         NOT NULL DEFAULT FALSE,
    favorite_home    VARCHAR(120)    NOT NULL DEFAULT '',
    favorite_center  VARCHAR(120)    NOT NULL DEFAULT '',
    favorite_therapy VARCHAR(120)    NOT NULL DEFAULT '',
    failed_logins    INT             NOT NULL DEFAULT 0,
    locked_until     TIMESTAMP(3)    NULL,
    created_at       TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at       TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB;

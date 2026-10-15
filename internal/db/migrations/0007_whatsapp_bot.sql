-- Chatbot de WhatsApp de los choferes: dónde va cada conversación (se identifica al chofer por el
-- teléfono registrado en el panel). Sobrevive a reinicios del API.
CREATE TABLE IF NOT EXISTS whatsapp_sessions (
    phone      VARCHAR(20)     NOT NULL PRIMARY KEY,
    driver_id  VARCHAR(16)     NOT NULL,
    step       VARCHAR(32)     NOT NULL DEFAULT '',
    draft      VARCHAR(1000)   NOT NULL DEFAULT '{}',
    shift_id   BIGINT UNSIGNED NULL,
    updated_at TIMESTAMP(3)    NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    CONSTRAINT fk_wa_driver FOREIGN KEY (driver_id) REFERENCES drivers(id) ON DELETE CASCADE,
    INDEX idx_wa_shift (shift_id)
) ENGINE=InnoDB;

-- Teléfonos vinculados a un titular.
--
-- Cada teléfono tiene DOS llaves, creadas en su chip seguro y que nunca salen de
-- él (a diferencia de una passkey, no se sincronizan a otros equipos):
--
--   biometric_key   solo firma tras la cara o la huella de la persona. Firmar
--                   con ella cubre los dos factores: tener ESTE teléfono y ser
--                   quien es.
--   possession_key  firma sin biometría. Sola no vale nada: es la prueba de que
--                   la clave de 6 dígitos se escribió en ESTE teléfono, para que
--                   nadie pueda probar claves desde otro lado.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE IF NOT EXISTS identity.devices (
    id                   UUID PRIMARY KEY,
    customer_id          UUID NOT NULL,
    name                 TEXT NOT NULL,
    -- Claves públicas P-256 en SubjectPublicKeyInfo (DER).
    biometric_key        BYTEA NOT NULL,
    possession_key       BYTEA NOT NULL,
    -- La clave de 6 dígitos, con Argon2id. Seis dígitos son un millón de
    -- combinaciones: el hash solo frena a quien robe la base, lo que protege de
    -- verdad es el bloqueo tras pocos intentos.
    pin_hash             BYTEA NOT NULL,
    pin_salt             BYTEA NOT NULL,
    failed_pin_attempts  INT NOT NULL DEFAULT 0,
    -- Lo declara la app; sin atestación (App Attest / Play Integrity) el banco no
    -- puede comprobarlo. Sirve para saber cuántos dispositivos viejos quedan.
    hardware_backed      BOOLEAN NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at         TIMESTAMPTZ,
    locked_at            TIMESTAMPTZ,
    revoked_at           TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_devices_customer ON identity.devices (customer_id);

-- Desafíos emitidos y todavía sin usar. Uno vale una vez, para un propósito.
CREATE TABLE IF NOT EXISTS identity.device_challenges (
    id           UUID PRIMARY KEY,
    purpose      TEXT NOT NULL CHECK (purpose IN ('register', 'login')),
    device_id    UUID REFERENCES identity.devices (id),
    payload      BYTEA NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ
);

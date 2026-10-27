-- Sesiones del canal del titular.
--
-- Las abre cualquier forma de entrar —passkey, llave del teléfono con biometría,
-- llave del teléfono con clave— y se guarda cuál fue: una operación sensible
-- podrá exigir que la sesión venga de la biometría y no de la clave.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE IF NOT EXISTS identity.app_sessions (
    -- SHA-256 del token. El token tiene 256 bits de entropía generados por el
    -- banco: un hash rápido basta, igual que con los secretos de integración.
    token_hash   TEXT PRIMARY KEY,
    customer_id  UUID NOT NULL,
    factor       TEXT NOT NULL CHECK (factor IN ('passkey', 'device_biometric', 'device_pin')),
    -- Qué firmó: la passkey (su id en base64url) o el dispositivo (su UUID).
    device_ref   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_app_sessions_device ON identity.app_sessions (device_ref);

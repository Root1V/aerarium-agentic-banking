-- Identidad del titular: passkeys, ceremonias y sesiones.
--
-- Vive en su propio esquema: el core no sabe quién es nadie, solo de cuentas, y
-- mezclar credenciales con el ledger pondría la pieza más sensible del banco al
-- alcance de cualquier consulta contable.

CREATE SCHEMA IF NOT EXISTS identity;

-- Una fila por passkey: un titular puede tener varias, una por dispositivo o por
-- llavero sincronizado.
CREATE TABLE IF NOT EXISTS identity.passkeys (
    credential_id  BYTEA PRIMARY KEY,
    customer_id    UUID NOT NULL,
    -- Nombre con el que el sistema operativo muestra la passkey en su llavero.
    user_name      TEXT NOT NULL,
    -- La credencial completa tal como la entiende la librería WebAuthn: clave
    -- pública, contador de firmas, banderas de respaldo. Se guarda entera porque
    -- verificar un login la necesita entera, y se reescribe tras cada uno.
    credential     JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_passkeys_customer ON identity.passkeys (customer_id);

-- Ceremonias en curso: el desafío que el servidor emitió y todavía no se usó.
--
-- En base de datos y no en memoria: con más de una instancia, el desafío lo
-- emitiría una y lo verificaría otra.
CREATE TABLE IF NOT EXISTS identity.ceremonies (
    id           UUID PRIMARY KEY,
    -- enrollment: primera passkey de un cliente recién aprobado.
    -- add_device: una passkey más, con sesión ya iniciada.
    -- login: entrar.
    kind         TEXT NOT NULL CHECK (kind IN ('enrollment', 'add_device', 'login')),
    customer_id  UUID,
    user_name    TEXT,
    session      JSONB NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    -- Se marca al usarse, de forma atómica: un desafío vale una vez. Sin esto,
    -- una respuesta capturada se podría reenviar.
    consumed_at  TIMESTAMPTZ
);

-- Sesiones emitidas tras una ceremonia superada.
CREATE TABLE IF NOT EXISTS identity.sessions (
    -- SHA-256 del token. El token tiene 256 bits de entropía generados por el
    -- banco: un hash rápido basta, igual que con los secretos de integración.
    token_hash     TEXT PRIMARY KEY,
    customer_id    UUID NOT NULL,
    credential_id  BYTEA NOT NULL REFERENCES identity.passkeys (credential_id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_customer ON identity.sessions (customer_id);

-- Credenciales de las integraciones que consumen las APIs de socio.
--
-- Una fila por INTEGRACIÓN, no por usuario final de esa integración. Mercatus
-- tiene un client_id; los agentes que operan sobre él se distinguen por cuenta,
-- no por credencial. Es lo que su equipo pidió y es lo correcto para una
-- plataforma: emitir una credencial por agente multiplicaría el número de
-- secretos vivos sin agregar aislamiento real, porque la plataforma puede actuar
-- por cualquiera de sus agentes de todos modos.
--
-- La contrapartida es que un token comprometido afecta a todos los agentes de esa
-- integración, y por eso el control tiene que estar en los límites por cuenta y
-- en la detección de anomalías, no solo en el scope.

CREATE SCHEMA IF NOT EXISTS oauth;

CREATE TABLE IF NOT EXISTS oauth.clients (
    client_id            TEXT PRIMARY KEY,
    name                 TEXT NOT NULL,

    -- SHA-256 del secreto, en hexadecimal.
    --
    -- No es bcrypt ni argon2 A PROPÓSITO, y la razón importa: esos algoritmos
    -- existen para encarecer la fuerza bruta contra secretos que eligió una
    -- persona, que tienen poca entropía. Este secreto lo genera el banco con 256
    -- bits de aleatoriedad criptográfica; adivinarlo es inviable con cualquier
    -- función de hash. Para una CONTRASEÑA humana esta decisión sería un error.
    secret_hash          TEXT NOT NULL,

    -- Rotación sin ventana de caída: el secreto anterior sigue sirviendo hasta
    -- previous_expires_at. Sin esto, rotar obliga a coordinar un despliegue
    -- simultáneo de los dos lados, que es exactamente cuando se rota mal.
    previous_secret_hash TEXT,
    previous_expires_at  TIMESTAMPTZ,

    -- Scopes que este cliente puede pedir. Pedir uno fuera de la lista es un
    -- error, no un scope silenciosamente recortado.
    scopes               TEXT[] NOT NULL DEFAULT '{}',

    -- Cuenta maestra de la integración. Toda cuenta que abra cuelga de esta.
    master_account_id    UUID,

    active               BOOLEAN NOT NULL DEFAULT TRUE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at           TIMESTAMPTZ
);

-- Sub-cuentas de una integración: qué agente controla cada cuenta del ledger.
--
-- El `owner_reference` es el identificador del agente en la plataforma del socio.
-- Debe ser trazable a una persona natural o jurídica real de su lado: el banco
-- hace diligencia sobre la integración, el socio responde por quién está detrás
-- de cada sub-cuenta.
CREATE TABLE IF NOT EXISTS oauth.client_accounts (
    client_id        TEXT NOT NULL REFERENCES oauth.clients (client_id),
    account_id       UUID NOT NULL,
    owner_reference  TEXT NOT NULL,
    currency         CHAR(3) NOT NULL,
    display_name     TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (client_id, account_id),
    -- Idempotencia de la apertura: reintentar POST /v1/accounts no puede crear
    -- una segunda sub-cuenta y partir el saldo del agente en dos.
    UNIQUE (client_id, owner_reference, currency)
);

CREATE INDEX IF NOT EXISTS idx_client_accounts_account
    ON oauth.client_accounts (account_id);

-- Solicitudes de consentimiento: el puente entre "la plataforma pide permiso" y
-- "el titular lo otorga".
--
-- La plataforma NO nombra la cuenta al pedir. Dice cuánto necesita y para qué; es
-- el titular quien elige, dentro de su app, sobre qué cuenta lo concede. Así la
-- plataforma no aprende identificadores de cuenta antes de tener permiso, y no
-- puede sondear qué cuentas existen probando peticiones.
CREATE TABLE IF NOT EXISTS oauth.consent_requests (
    id                 UUID PRIMARY KEY,
    client_id          TEXT NOT NULL REFERENCES oauth.clients (client_id),

    -- Lo que el titular usa para encontrar la solicitud en su app. Alta entropía
    -- porque viaja fuera de banda (un enlace, un QR) y no está detrás de un token.
    handoff_code       TEXT NOT NULL UNIQUE,

    -- Lo que la plataforma PIDE. El titular puede conceder menos, nunca más.
    requested_max_per_operation_micros BIGINT,
    requested_max_total_micros         BIGINT,
    currency           CHAR(3) NOT NULL,
    -- Para qué, en palabras que una persona entienda. Se muestra en pantalla.
    purpose            TEXT NOT NULL DEFAULT '',

    -- PENDING · APPROVED · REJECTED. "Vencida" se deriva de expires_at, igual que
    -- en autorizaciones y mandatos.
    status             TEXT NOT NULL DEFAULT 'PENDING',
    -- El mandato que nació de aprobarla.
    mandate_id         UUID,
    account_id         UUID,

    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at         TIMESTAMPTZ NOT NULL,
    resolved_at        TIMESTAMPTZ,

    CONSTRAINT approved_has_mandate
        CHECK ((status = 'APPROVED') = (mandate_id IS NOT NULL)),
    CONSTRAINT resolved_has_timestamp
        CHECK ((status = 'PENDING') = (resolved_at IS NULL))
);

CREATE INDEX IF NOT EXISTS idx_consent_requests_client
    ON oauth.consent_requests (client_id, created_at DESC);

-- Los agentes de cada plataforma y las claves con las que firman sus pagos
-- (AE-10 en el canal con Mercatus, F3-05).
--
-- La plataforma se autentica con su credencial OAuth; el agente, además, firma
-- cada pago con su propia clave. Sin firma, decir qué agente paga es solo lo que
-- dice la plataforma. Con ella, es una prueba.

CREATE SCHEMA IF NOT EXISTS agents;

CREATE TABLE IF NOT EXISTS agents.agents (
    id          UUID PRIMARY KEY,
    -- La integración dueña del agente (oauth.clients.client_id).
    client_id   TEXT NOT NULL,
    -- Lo que ve el titular al conceder un permiso: tiene que reconocerlo.
    name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Dado de baja: sus claves y sus permisos dejan de servir al momento.
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_agents_client ON agents.agents (client_id);

CREATE TABLE IF NOT EXISTS agents.keys (
    id          UUID PRIMARY KEY,
    agent_id    UUID NOT NULL REFERENCES agents.agents (id),
    -- Ed25519, los 32 bytes de la clave pública.
    public_key  BYTEA NOT NULL CHECK (length(public_key) = 32),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Rotación sin baja: al añadir una clave, las anteriores siguen valiendo
    -- hasta `expires_at` (7 días) o hasta que la plataforma las retire.
    expires_at  TIMESTAMPTZ,
    retired_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_agent_keys_agent ON agents.keys (agent_id);
-- Una misma clave no puede ser de dos agentes: la firma no diría de quién es.
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_keys_public ON agents.keys (public_key);

-- Modelo A: la sub-cuenta con la que paga un agente es suya. Solo él la puede
-- usar como pagadora. Una sub-cuenta sin agente (la de un vendedor) cobra, pero
-- no paga.
CREATE TABLE IF NOT EXISTS agents.accounts (
    account_id  UUID PRIMARY KEY,
    agent_id    UUID NOT NULL REFERENCES agents.agents (id),
    bound_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Por qué se hizo cada pago: la tarea que el cliente le dio al agente y qué se
-- compró. Lo lee el titular tal cual en su historial.
CREATE TABLE IF NOT EXISTS agents.payment_intents (
    authorization_id UUID PRIMARY KEY,
    agent_id         UUID NOT NULL REFERENCES agents.agents (id),
    client_id        TEXT NOT NULL,
    reference        TEXT NOT NULL CHECK (length(reference) BETWEEN 1 AND 128),
    description      TEXT NOT NULL CHECK (length(description) BETWEEN 1 AND 140),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

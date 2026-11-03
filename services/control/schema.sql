-- Registro de decisiones de la capa de control.
--
-- Cada vez que el banco decide si una operación sigue, pide confirmación o se
-- niega, queda aquí: quién la pidió, qué, con qué nivel, qué se decidió y por
-- qué. Y cuando pidió confirmación, si se confirmó. Es lo que se presenta si
-- alguien pregunta por qué el banco dejó —o no dejó— mover su dinero.
--
-- De solo agregar, como el ledger: un registro que se puede editar no prueba nada.

CREATE SCHEMA IF NOT EXISTS control;

CREATE TABLE IF NOT EXISTS control.decisions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    decided_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    customer_id  UUID NOT NULL,
    -- Quién pidió la operación: la persona, el agente de una plataforma, una
    -- regla automática o el asistente.
    actor_kind   TEXT NOT NULL CHECK (actor_kind IN ('holder', 'agent', 'rule', 'assistant')),
    actor_id     TEXT NOT NULL DEFAULT '',
    action       TEXT NOT NULL,
    -- 0: acción fuera del catálogo, que se niega siempre.
    level        SMALLINT NOT NULL CHECK (level BETWEEN 0 AND 4),
    outcome      TEXT NOT NULL CHECK (outcome IN ('allow', 'confirm', 'deny', 'confirmed', 'rejected')),
    reason       TEXT NOT NULL DEFAULT '',
    -- La descripción canónica de la operación: la misma que firma el teléfono.
    operation    TEXT NOT NULL DEFAULT '',
    -- Con qué se decidió: el permiso usado, el teléfono que firmó y cómo.
    evidence     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_decisions_customer ON control.decisions (customer_id, decided_at DESC);

CREATE OR REPLACE FUNCTION control.forbid_decision_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'control.decisions es de solo agregar: % no está permitido', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_decisions_append_only ON control.decisions;
CREATE TRIGGER trg_decisions_append_only
    BEFORE UPDATE OR DELETE ON control.decisions
    FOR EACH ROW EXECUTE FUNCTION control.forbid_decision_mutation();

-- Cómo se autorizó cada operación que se ejecutó: el enlace entre un movimiento
-- del historial (su número de operación) y la decisión que lo permitió. Es lo
-- que deja al titular ver, en cada movimiento, quién lo hizo y con qué prueba.
CREATE TABLE IF NOT EXISTS control.executions (
    transaction_id UUID PRIMARY KEY,
    customer_id    UUID NOT NULL,
    actor_kind     TEXT NOT NULL CHECK (actor_kind IN ('holder', 'agent', 'rule', 'assistant')),
    actor_id       TEXT NOT NULL,
    action         TEXT NOT NULL,
    -- `allow` (no pidió confirmación) o `confirmed` (la persona lo confirmó).
    outcome        TEXT NOT NULL CHECK (outcome IN ('allow', 'confirmed')),
    reason         TEXT NOT NULL DEFAULT '',
    evidence       TEXT NOT NULL DEFAULT '',
    executed_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

DROP TRIGGER IF EXISTS trg_executions_append_only ON control.executions;
CREATE TRIGGER trg_executions_append_only
    BEFORE UPDATE OR DELETE ON control.executions
    FOR EACH ROW EXECUTE FUNCTION control.forbid_decision_mutation();

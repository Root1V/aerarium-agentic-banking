-- =============================================================================
-- AIBank core — V004: conciliación
--
-- La conciliación es la razón por la que este core mantiene su propio ledger en
-- vez de confiar en el registro del proveedor. Sin un registro propio no hay nada
-- contra qué comparar, y una diferencia solo se descubre cuando alguien reclama.
--
-- Tres clases de diferencia, en orden de gravedad:
--   1. El ledger no cuadra consigo mismo (saldo materializado vs asientos).
--   2. El proveedor tiene un movimiento que nosotros no, o al revés.
--   3. Dinero parado demasiado tiempo en una cuenta de tránsito o retención.
--
-- Los hallazgos se guardan porque conciliar sin dejar rastro no sirve: hay que
-- poder demostrar qué se detectó, cuándo y cómo se resolvió.
-- =============================================================================

CREATE TYPE reconciliation_status AS ENUM ('RUNNING', 'COMPLETED', 'FAILED');

CREATE TYPE finding_kind AS ENUM (
    -- El saldo materializado no coincide con la suma de los asientos.
    'BALANCE_DRIFT',
    -- El proveedor registra un movimiento que no está en el ledger.
    'MISSING_IN_LEDGER',
    -- El ledger registra un movimiento que el proveedor no reporta.
    'MISSING_AT_PROVIDER',
    -- Ambos lo tienen, por importes distintos.
    'AMOUNT_MISMATCH',
    -- Dinero detenido en tránsito o retenido más allá del plazo razonable.
    'STALE_SUSPENSE'
);

CREATE TABLE reconciliation_runs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Qué se concilió: "internal", "rail:sim", "card:sim", ...
    scope       TEXT NOT NULL,
    status      reconciliation_status NOT NULL DEFAULT 'RUNNING',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    -- Recuento por tipo de hallazgo, para no tener que agregar en cada consulta.
    summary     JSONB NOT NULL DEFAULT '{}'::jsonb,
    error       TEXT
);

CREATE INDEX idx_reconciliation_runs_scope ON reconciliation_runs (scope, started_at DESC);

CREATE TABLE reconciliation_findings (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id         UUID NOT NULL REFERENCES reconciliation_runs (id),
    kind           finding_kind NOT NULL,
    account_id     UUID REFERENCES accounts (id),
    -- Referencia externa (clave de idempotencia, id de la red) cuando aplica.
    reference      TEXT,
    -- Lo que debería ser y lo que hay. Siempre en micras (10^-6).
    expected_micros BIGINT,
    actual_micros   BIGINT,
    currency       CHAR(3),
    detail         TEXT NOT NULL,
    -- Una diferencia se resuelve con una decisión humana o un asiento de ajuste;
    -- hasta entonces queda abierta.
    resolved_at    TIMESTAMPTZ,
    resolution     TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_findings_open ON reconciliation_findings (kind) WHERE resolved_at IS NULL;
CREATE INDEX idx_findings_run ON reconciliation_findings (run_id);

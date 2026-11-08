-- Metas con ahorro automático autorizado (F3-03).
--
-- La meta es un bolsillo con un monto y, si se quiere, una fecha. La regla de
-- ahorro aparta en él cada semana o cada mes, dentro de un permiso que la
-- persona confirma con la cara y retira cuando quiere. Antes de cada aporte se
-- mira la previsión de saldo: la regla no aparta lo que la dejaría corta antes de
-- cobrar.

CREATE SCHEMA IF NOT EXISTS goals;

CREATE TABLE IF NOT EXISTS goals.goals (
    id            UUID PRIMARY KEY,
    customer_id   UUID NOT NULL,
    account_id    UUID NOT NULL,
    -- El bolsillo donde se junta: el saldo de la meta es el suyo.
    pocket_id     UUID NOT NULL UNIQUE,
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    currency      TEXT NOT NULL,
    target_micros BIGINT NOT NULL CHECK (target_micros > 0),
    target_date   DATE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    reached_at    TIMESTAMPTZ,
    closed_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_goals_account ON goals.goals (account_id) WHERE closed_at IS NULL;

CREATE TABLE IF NOT EXISTS goals.rules (
    id            UUID PRIMARY KEY,
    goal_id       UUID NOT NULL REFERENCES goals.goals (id),
    customer_id   UUID NOT NULL,
    -- Lo que aparta cada vez. Es también el tope del permiso: la regla nunca
    -- aparta más que esto en un aporte.
    amount_micros BIGINT NOT NULL CHECK (amount_micros > 0),
    cadence       TEXT NOT NULL CHECK (cadence IN ('weekly', 'monthly')),
    -- El día del mes de los aportes mensuales (1–31; en un mes más corto, su
    -- último día).
    anchor_day    SMALLINT NOT NULL CHECK (anchor_day BETWEEN 1 AND 31),
    next_run_on   DATE NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Cómo la autorizó la persona: el teléfono y el método.
    evidence      TEXT NOT NULL DEFAULT '',
    revoked_at    TIMESTAMPTZ,
    -- `holder` (la quitó la persona), `goal_reached`, `goal_closed`.
    revoked_reason TEXT
);

-- Una regla viva por meta: dos harían ambiguo cuánto se aparta.
CREATE UNIQUE INDEX IF NOT EXISTS idx_rules_live ON goals.rules (goal_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_rules_due ON goals.rules (next_run_on) WHERE revoked_at IS NULL;

-- Lo que hizo la regla cada vez que le tocó, también cuando no apartó: la
-- persona tiene que poder ver por qué un aporte no se hizo.
CREATE TABLE IF NOT EXISTS goals.contributions (
    rule_id        UUID NOT NULL REFERENCES goals.rules (id),
    -- La semana (2026-W45) o el mes (2026-11): un aporte por periodo.
    period         TEXT NOT NULL,
    run_on         DATE NOT NULL,
    -- `saved`, `reduced` (menos de lo pactado, para no dejarla corta),
    -- `skipped` (nada) o `reached` (la meta ya estaba cumplida).
    outcome        TEXT NOT NULL CHECK (outcome IN ('saved', 'reduced', 'skipped', 'reached')),
    asked_micros   BIGINT NOT NULL,
    amount_micros  BIGINT NOT NULL DEFAULT 0,
    -- `keeps_cushion`, `insufficient_funds`, `not_allowed`, o vacío.
    reason         TEXT NOT NULL DEFAULT '',
    -- Con qué se decidió: el punto más bajo previsto y el ingreso esperado.
    lowest_micros  BIGINT,
    income_on      DATE,
    transaction_id UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (rule_id, period)
);

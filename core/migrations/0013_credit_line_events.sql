-- =============================================================================
-- AIBank core — V013: constancia de cada bloqueo y desbloqueo por mora
--
-- Bloquear una tarjeta por mora es una decisión automática que afecta a una
-- persona: tiene que poder explicarse después, también a un supervisor. Hasta
-- aquí solo cambiaba el estado de la línea y no quedaba ni cuándo ni por qué.
--
-- Cada cambio de estado deja una fila con lo que lo decidió: qué estado de cuenta
-- mandaba, cuánto era el mínimo, cuánto se había pagado y a qué fecha se revisó.
-- Se escribe en la misma transacción que el cambio: no hay bloqueo sin
-- constancia. De solo agregar, como el ledger.
-- =============================================================================

CREATE TABLE credit_line_events (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    line_id             UUID NOT NULL REFERENCES credit_lines (id),
    customer_id         UUID NOT NULL,
    from_status         credit_line_status NOT NULL,
    to_status           credit_line_status NOT NULL,
    -- `minimum_not_paid`: venció el estado de cuenta sin cubrir el mínimo.
    -- `caught_up`: se cubrió el mínimo del que mandaba, o ya no hay uno vencido.
    reason              TEXT NOT NULL CHECK (reason IN ('minimum_not_paid', 'caught_up')),
    statement_id        UUID REFERENCES credit_statements (id),
    due_date            DATE,
    currency            TEXT NOT NULL,
    minimum_micros      BIGINT,
    paid_micros         BIGINT,
    -- La fecha a la que se revisó (en el sandbox, «pasar el vencimiento» la adelanta).
    as_of               TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_credit_line_events_line ON credit_line_events (line_id, id DESC);
CREATE INDEX idx_credit_line_events_created ON credit_line_events (created_at DESC);

CREATE FUNCTION forbid_credit_line_event_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'credit_line_events is append-only: % is forbidden', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_credit_line_events_append_only
    BEFORE UPDATE OR DELETE ON credit_line_events
    FOR EACH ROW EXECUTE FUNCTION forbid_credit_line_event_mutation();

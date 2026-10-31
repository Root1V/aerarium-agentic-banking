-- =============================================================================
-- AIBank core — V011: ciclo de la tarjeta de crédito
--
-- El estado de cuenta es la foto de la línea al corte: con qué se abrió el
-- periodo, qué se usó, qué se pagó, qué intereses se cobraron, con qué cerró, y
-- cuánto hay que pagar como mínimo y hasta cuándo. Una vez emitido no cambia: lo
-- que pase después va al siguiente.
-- =============================================================================

-- Pago mínimo del contrato: un porcentaje de lo que se debe al corte, más los
-- intereses, con un piso. Se fijan al abrir la línea, como la tasa.
ALTER TABLE credit_lines
    ADD COLUMN min_payment_bps          INT    NOT NULL DEFAULT 500 CHECK (min_payment_bps BETWEEN 1 AND 10000),
    ADD COLUMN min_payment_floor_micros BIGINT NOT NULL DEFAULT 10000000 CHECK (min_payment_floor_micros >= 0);

CREATE TABLE credit_statements (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    line_id                 UUID NOT NULL REFERENCES credit_lines (id),
    period_start            TIMESTAMPTZ NOT NULL,
    -- El instante del corte.
    period_end              TIMESTAMPTZ NOT NULL,
    opening_micros          BIGINT NOT NULL,
    charges_micros          BIGINT NOT NULL,
    payments_micros         BIGINT NOT NULL,
    interest_micros         BIGINT NOT NULL CHECK (interest_micros >= 0),
    closing_micros          BIGINT NOT NULL,
    minimum_payment_micros  BIGINT NOT NULL CHECK (minimum_payment_micros >= 0),
    due_date                DATE NOT NULL,
    -- El asiento de los intereses, si hubo.
    interest_transaction_id UUID REFERENCES ledger_transactions (id),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT statement_period CHECK (period_end > period_start),
    CONSTRAINT statement_balances CHECK (
        closing_micros = opening_micros + charges_micros + interest_micros - payments_micros
    ),
    UNIQUE (line_id, period_end)
);

CREATE INDEX credit_statements_line ON credit_statements (line_id, period_end DESC);

-- Un estado de cuenta emitido es un documento: no se edita ni se borra.
CREATE FUNCTION credit_statements_immutable() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'credit statements are immutable: % on %', TG_OP, OLD.id;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_credit_statements_immutable
    BEFORE UPDATE OR DELETE ON credit_statements
    FOR EACH ROW EXECUTE FUNCTION credit_statements_immutable();

-- El pago mínimo es parte del contrato, como la tasa: tampoco cambia.
CREATE OR REPLACE FUNCTION credit_lines_guard() RETURNS trigger AS $$
BEGIN
    IF NEW.account_id <> OLD.account_id OR NEW.customer_id <> OLD.customer_id
       OR NEW.annual_rate_bps <> OLD.annual_rate_bps OR NEW.decision_ref <> OLD.decision_ref
       OR NEW.opened_at <> OLD.opened_at
       OR NEW.min_payment_bps <> OLD.min_payment_bps
       OR NEW.min_payment_floor_micros <> OLD.min_payment_floor_micros THEN
        RAISE EXCEPTION 'credit line % terms are immutable', OLD.id;
    END IF;
    IF OLD.status = 'CLOSED' THEN
        RAISE EXCEPTION 'credit line % is closed', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- Límites: los intereses se cobran aunque la línea esté bloqueada o la deuda
-- quede por encima del límite. Son consecuencia de lo que ya se debe, no un uso
-- nuevo: si el límite los rechazara, el banco dejaría de cobrar justo a quien
-- más se atrasa.
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION check_account_limits() RETURNS trigger AS $$
DECLARE
    acc RECORD;
    bal BIGINT;
    prod RECORD;
    line RECORD;
    tx_kind TEXT;
BEGIN
    SELECT allows_overdraft, product_id INTO acc FROM accounts WHERE id = NEW.account_id;
    SELECT balance_micros INTO bal FROM account_balances WHERE account_id = NEW.account_id;

    SELECT limit_micros, status INTO line FROM credit_lines WHERE account_id = NEW.account_id;
    IF FOUND THEN
        SELECT kind INTO tx_kind FROM ledger_transactions WHERE id = NEW.transaction_id;
        IF tx_kind <> 'credit_interest' THEN
            IF NEW.direction = 'DEBIT' AND line.status <> 'ACTIVE' THEN
                RAISE EXCEPTION 'credit line on account % is %: it does not accept new charges',
                    NEW.account_id, line.status
                    USING ERRCODE = 'AB011';
            END IF;
            IF NEW.direction = 'DEBIT' AND bal > line.limit_micros THEN
                RAISE EXCEPTION 'credit limit exceeded: account % would owe %, limit %',
                    NEW.account_id, bal, line.limit_micros
                    USING ERRCODE = 'AB010';
            END IF;
        END IF;
        IF bal < 0 THEN
            RAISE EXCEPTION 'payment exceeds debt: account % would end at %', NEW.account_id, bal
                USING ERRCODE = 'AB012';
        END IF;
    END IF;

    IF NOT acc.allows_overdraft AND bal < 0 THEN
        RAISE EXCEPTION 'insufficient funds: account % would end at %', NEW.account_id, bal
            USING ERRCODE = 'AB001';
    END IF;

    IF acc.product_id IS NOT NULL THEN
        SELECT max_balance_micros, max_transaction_micros INTO prod
        FROM products WHERE id = acc.product_id;

        IF prod.max_balance_micros IS NOT NULL AND bal > prod.max_balance_micros THEN
            RAISE EXCEPTION 'balance cap exceeded: account % would end at %, cap %',
                NEW.account_id, bal, prod.max_balance_micros
                USING ERRCODE = 'AB002';
        END IF;

        IF prod.max_transaction_micros IS NOT NULL
           AND NEW.amount_micros > prod.max_transaction_micros THEN
            RAISE EXCEPTION 'transaction cap exceeded: account % amount %, cap %',
                NEW.account_id, NEW.amount_micros, prod.max_transaction_micros
                USING ERRCODE = 'AB003';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

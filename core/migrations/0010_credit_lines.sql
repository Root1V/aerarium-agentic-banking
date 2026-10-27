-- =============================================================================
-- AIBank core — V010: líneas de crédito (tarjeta de crédito)
--
-- Una línea de crédito es un contrato sobre una cuenta del cliente de tipo
-- ACTIVO: lo que el cliente le debe al banco. Usar la línea la debita (la deuda
-- crece); pagarla la acredita (la deuda baja). El saldo de esa cuenta ES la deuda.
--
-- Las reglas viven donde viven las demás reglas de saldo: en el trigger diferido
-- que se evalúa al COMMIT sobre el saldo final. Así ningún camino —una
-- disposición, una compra con tarjeta, un ajuste— puede saltarse el límite.
--
--   AB010 límite de crédito superado
--   AB011 la línea no está activa (bloqueada por mora o cerrada): no admite usos
--   AB012 el pago supera la deuda
-- =============================================================================

ALTER TYPE product_kind ADD VALUE IF NOT EXISTS 'CREDIT_CARD';

CREATE TYPE credit_line_status AS ENUM ('ACTIVE', 'BLOCKED', 'CLOSED');

CREATE TABLE credit_lines (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id       UUID NOT NULL UNIQUE REFERENCES accounts (id),
    customer_id      UUID NOT NULL,
    limit_micros     BIGINT NOT NULL CHECK (limit_micros > 0),
    -- Tasa efectiva anual en puntos básicos (4500 = 45 %). Es la del contrato: se
    -- fija al abrir la línea y no cambia con el producto.
    annual_rate_bps  INT NOT NULL CHECK (annual_rate_bps >= 0),
    -- Día del mes en que se corta el estado de cuenta, y días hasta el
    -- vencimiento del pago. Hasta 28: todos los meses tienen ese día.
    cycle_day        SMALLINT NOT NULL CHECK (cycle_day BETWEEN 1 AND 28),
    payment_days     SMALLINT NOT NULL CHECK (payment_days BETWEEN 1 AND 28),
    status           credit_line_status NOT NULL DEFAULT 'ACTIVE',
    -- Referencia al expediente de la decisión de crédito que la otorgó: sin ella
    -- no se puede reconstruir por qué esta persona tiene este límite.
    decision_ref     TEXT NOT NULL,
    opened_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Una línea viva por cliente.
CREATE UNIQUE INDEX credit_lines_one_open_per_customer
    ON credit_lines (customer_id) WHERE status <> 'CLOSED';

-- El límite se puede cambiar (subirlo por buen comportamiento, bajarlo por
-- riesgo) y el estado también; nada más del contrato.
CREATE FUNCTION credit_lines_guard() RETURNS trigger AS $$
BEGIN
    IF NEW.account_id <> OLD.account_id OR NEW.customer_id <> OLD.customer_id
       OR NEW.annual_rate_bps <> OLD.annual_rate_bps OR NEW.decision_ref <> OLD.decision_ref
       OR NEW.opened_at <> OLD.opened_at THEN
        RAISE EXCEPTION 'credit line % terms are immutable', OLD.id;
    END IF;
    IF OLD.status = 'CLOSED' THEN
        RAISE EXCEPTION 'credit line % is closed', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_credit_lines_guard
    BEFORE UPDATE ON credit_lines
    FOR EACH ROW EXECUTE FUNCTION credit_lines_guard();

-- -----------------------------------------------------------------------------
-- Límites: la misma función de V002, más las reglas de crédito.
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION check_account_limits() RETURNS trigger AS $$
DECLARE
    acc RECORD;
    bal BIGINT;
    prod RECORD;
    line RECORD;
BEGIN
    SELECT allows_overdraft, product_id INTO acc FROM accounts WHERE id = NEW.account_id;
    SELECT balance_micros INTO bal FROM account_balances WHERE account_id = NEW.account_id;

    -- Crédito primero: en una cuenta de crédito un saldo negativo no es "fondos
    -- insuficientes" sino haber pagado de más, y hay que decirlo así.
    SELECT limit_micros, status INTO line FROM credit_lines WHERE account_id = NEW.account_id;
    IF FOUND THEN
        IF NEW.direction = 'DEBIT' AND line.status <> 'ACTIVE' THEN
            RAISE EXCEPTION 'credit line on account % is %: it does not accept new charges',
                NEW.account_id, line.status
                USING ERRCODE = 'AB011';
        END IF;
        IF bal > line.limit_micros THEN
            RAISE EXCEPTION 'credit limit exceeded: account % would owe %, limit %',
                NEW.account_id, bal, line.limit_micros
                USING ERRCODE = 'AB010';
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

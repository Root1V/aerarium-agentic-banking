-- =============================================================================
-- AIBank core — V002: catálogo de productos, saldos materializados y límites
--
-- Añade tres cosas que el ledger puro no cubre y un banco real necesita:
--   (1) Productos como CONFIGURACIÓN: abrir una cuenta hereda moneda y reglas
--       del producto; un producto nuevo no requiere código nuevo.
--   (2) Saldo materializado por cuenta, mantenido transaccionalmente por trigger.
--       El UPDATE toma lock de fila, lo que SERIALIZA los movimientos sobre la
--       misma cuenta — condición necesaria para impedir sobregiro bajo concurrencia.
--       Sigue siendo verificable: debe coincidir con account_balance_projection.
--   (3) Límites: prohibición de sobregiro y topes regulatorios de saldo/operación
--       (cuentas simplificadas de la región: nivel 1 en México, EEDE en Perú).
--
-- Los errores de negocio usan SQLSTATE propios para que la aplicación los
-- distinga sin parsear mensajes:
--   AB001 fondos insuficientes · AB002 tope de saldo · AB003 tope por operación
-- =============================================================================

CREATE TYPE product_kind AS ENUM ('DEPOSIT_ACCOUNT');

CREATE TABLE products (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                  TEXT NOT NULL UNIQUE,
    name                  TEXT NOT NULL,
    kind                  product_kind NOT NULL,
    currency              CHAR(3) NOT NULL,
    allows_overdraft      BOOLEAN NOT NULL DEFAULT FALSE,
    max_balance_minor     BIGINT,
    max_transaction_minor BIGINT,
    active                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT positive_caps CHECK (
        (max_balance_minor IS NULL OR max_balance_minor > 0) AND
        (max_transaction_minor IS NULL OR max_transaction_minor > 0)
    )
);

ALTER TABLE accounts
    ADD COLUMN product_id       UUID REFERENCES products (id),
    ADD COLUMN allows_overdraft BOOLEAN NOT NULL DEFAULT FALSE;

-- Una cuenta de cliente siempre nace de un producto; las internas no.
ALTER TABLE accounts ADD CONSTRAINT customer_account_needs_product
    CHECK (owner <> 'CUSTOMER' OR product_id IS NOT NULL);

-- -----------------------------------------------------------------------------
-- Saldo materializado
-- -----------------------------------------------------------------------------
DROP VIEW account_balances;

CREATE TABLE account_balances (
    account_id    UUID PRIMARY KEY REFERENCES accounts (id),
    balance_minor BIGINT NOT NULL DEFAULT 0,
    entry_count   BIGINT NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE FUNCTION create_balance_row() RETURNS trigger AS $$
BEGIN
    INSERT INTO account_balances (account_id) VALUES (NEW.id);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_accounts_balance_row
    AFTER INSERT ON accounts
    FOR EACH ROW EXECUTE FUNCTION create_balance_row();

-- El saldo natural depende del tipo: ASSET/EXPENSE crecen al debe; el resto al haber.
CREATE FUNCTION apply_entry_to_balance() RETURNS trigger AS $$
DECLARE
    acc_type account_type;
    delta    BIGINT;
BEGIN
    SELECT type INTO acc_type FROM accounts WHERE id = NEW.account_id;

    delta := CASE
        WHEN acc_type IN ('ASSET', 'EXPENSE') THEN
            CASE WHEN NEW.direction = 'DEBIT' THEN NEW.amount_minor ELSE -NEW.amount_minor END
        ELSE
            CASE WHEN NEW.direction = 'CREDIT' THEN NEW.amount_minor ELSE -NEW.amount_minor END
    END;

    -- Este UPDATE toma lock de la fila de saldo: dos movimientos sobre la misma
    -- cuenta se serializan aquí, que es lo que hace fiable el control de sobregiro.
    UPDATE account_balances
       SET balance_minor = balance_minor + delta,
           entry_count   = entry_count + 1,
           updated_at    = now()
     WHERE account_id = NEW.account_id;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_entries_apply_balance
    AFTER INSERT ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION apply_entry_to_balance();

-- -----------------------------------------------------------------------------
-- Límites: se evalúan DIFERIDOS (al COMMIT) sobre el saldo final, para no
-- rechazar estados intermedios legítimos dentro de una misma transacción.
-- -----------------------------------------------------------------------------
CREATE FUNCTION check_account_limits() RETURNS trigger AS $$
DECLARE
    acc RECORD;
    bal BIGINT;
    prod RECORD;
BEGIN
    SELECT allows_overdraft, product_id INTO acc FROM accounts WHERE id = NEW.account_id;
    SELECT balance_minor INTO bal FROM account_balances WHERE account_id = NEW.account_id;

    IF NOT acc.allows_overdraft AND bal < 0 THEN
        RAISE EXCEPTION 'insufficient funds: account % would end at %', NEW.account_id, bal
            USING ERRCODE = 'AB001';
    END IF;

    IF acc.product_id IS NOT NULL THEN
        SELECT max_balance_minor, max_transaction_minor INTO prod
        FROM products WHERE id = acc.product_id;

        IF prod.max_balance_minor IS NOT NULL AND bal > prod.max_balance_minor THEN
            RAISE EXCEPTION 'balance cap exceeded: account % would end at %, cap %',
                NEW.account_id, bal, prod.max_balance_minor
                USING ERRCODE = 'AB002';
        END IF;

        IF prod.max_transaction_minor IS NOT NULL
           AND NEW.amount_minor > prod.max_transaction_minor THEN
            RAISE EXCEPTION 'transaction cap exceeded: account % amount %, cap %',
                NEW.account_id, NEW.amount_minor, prod.max_transaction_minor
                USING ERRCODE = 'AB003';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_entries_account_limits
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_account_limits();

-- -----------------------------------------------------------------------------
-- Proyección desde los asientos: fuente de verdad para auditar el saldo
-- materializado. Deben coincidir siempre (se verifica en tests y conciliación).
-- -----------------------------------------------------------------------------
CREATE VIEW account_balance_projection AS
SELECT
    a.id AS account_id,
    CASE WHEN a.type IN ('ASSET', 'EXPENSE')
         THEN COALESCE(SUM(CASE WHEN e.direction = 'DEBIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)
         ELSE COALESCE(SUM(CASE WHEN e.direction = 'CREDIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)
    END::BIGINT AS projected_minor,
    COUNT(e.id)::BIGINT AS projected_entries
FROM accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id
GROUP BY a.id;

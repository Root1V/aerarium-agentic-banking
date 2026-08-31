-- =============================================================================
-- AIBank core ledger — V001
-- Libro de doble partida, append-only. Invariantes aplicadas en la base:
--   (1) toda transacción balancea (Σ débitos = Σ créditos por moneda)
--   (2) los asientos son inmutables (sin UPDATE/DELETE)
--   (3) la moneda del asiento coincide con la de la cuenta
--   (4) idempotencia por clave única de operación
-- =============================================================================

CREATE TYPE account_type AS ENUM ('ASSET', 'LIABILITY', 'EQUITY', 'INCOME', 'EXPENSE');
CREATE TYPE account_owner AS ENUM ('CUSTOMER', 'INTERNAL');
CREATE TYPE account_status AS ENUM ('ACTIVE', 'FROZEN', 'CLOSED');
CREATE TYPE entry_direction AS ENUM ('DEBIT', 'CREDIT');

-- -----------------------------------------------------------------------------
-- Cuentas (de clientes e internas: caja, settlement con proveedores, comisiones)
-- -----------------------------------------------------------------------------
CREATE TABLE accounts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    type        account_type NOT NULL,
    owner       account_owner NOT NULL,
    owner_id    UUID,
    currency    CHAR(3) NOT NULL,
    status      account_status NOT NULL DEFAULT 'ACTIVE',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT customer_account_needs_owner
        CHECK (owner <> 'CUSTOMER' OR owner_id IS NOT NULL)
);

CREATE INDEX idx_accounts_owner ON accounts (owner, owner_id);

-- -----------------------------------------------------------------------------
-- Transacciones contables
-- request_hash detecta reutilización de idempotency_key con payload distinto.
-- -----------------------------------------------------------------------------
CREATE TABLE ledger_transactions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash    TEXT NOT NULL,
    kind            TEXT NOT NULL,
    description     TEXT,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    posted_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------------
-- Asientos (2+ por transacción). Montos SIEMPRE en unidades menores (centavos),
-- enteros positivos; la dirección va en `direction`. Nunca floats.
-- -----------------------------------------------------------------------------
CREATE TABLE ledger_entries (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id UUID NOT NULL REFERENCES ledger_transactions (id),
    account_id     UUID NOT NULL REFERENCES accounts (id),
    direction      entry_direction NOT NULL,
    amount_minor   BIGINT NOT NULL CHECK (amount_minor > 0),
    currency       CHAR(3) NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_entries_account ON ledger_entries (account_id, id);
CREATE INDEX idx_entries_tx ON ledger_entries (transaction_id);

-- -----------------------------------------------------------------------------
-- Invariante (2): append-only. Toda corrección es un asiento de reversa.
-- -----------------------------------------------------------------------------
CREATE FUNCTION ledger_forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger is append-only: % on % is forbidden', TG_OP, TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_entries_append_only
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();

CREATE TRIGGER trg_transactions_append_only
    BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();

-- -----------------------------------------------------------------------------
-- Invariante (3): la moneda del asiento debe coincidir con la de la cuenta,
-- y la cuenta no puede estar cerrada.
-- -----------------------------------------------------------------------------
CREATE FUNCTION ledger_check_entry_account() RETURNS trigger AS $$
DECLARE
    acc RECORD;
BEGIN
    SELECT currency, status INTO acc FROM accounts WHERE id = NEW.account_id;
    IF acc.currency IS DISTINCT FROM NEW.currency THEN
        RAISE EXCEPTION 'entry currency % does not match account currency %',
            NEW.currency, acc.currency;
    END IF;
    IF acc.status = 'CLOSED' THEN
        RAISE EXCEPTION 'account % is closed', NEW.account_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_entries_account_check
    BEFORE INSERT ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_check_entry_account();

-- -----------------------------------------------------------------------------
-- Invariante (1): la transacción balancea por moneda y tiene 2+ asientos.
-- Constraint trigger DIFERIDO: se evalúa al commit, cuando todos los asientos
-- de la transacción ya están insertados.
-- -----------------------------------------------------------------------------
CREATE FUNCTION ledger_check_balanced() RETURNS trigger AS $$
DECLARE
    unbalanced INT;
    n_entries  INT;
BEGIN
    SELECT COUNT(*) INTO unbalanced
    FROM (
        SELECT currency
        FROM ledger_entries
        WHERE transaction_id = NEW.transaction_id
        GROUP BY currency
        HAVING SUM(CASE WHEN direction = 'DEBIT' THEN amount_minor ELSE -amount_minor END) <> 0
    ) x;
    IF unbalanced > 0 THEN
        RAISE EXCEPTION 'transaction % is unbalanced', NEW.transaction_id;
    END IF;

    SELECT COUNT(*) INTO n_entries
    FROM ledger_entries WHERE transaction_id = NEW.transaction_id;
    IF n_entries < 2 THEN
        RAISE EXCEPTION 'transaction % must have at least 2 entries', NEW.transaction_id;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_entries_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_balanced();

-- -----------------------------------------------------------------------------
-- Saldos como proyección del ledger (nunca un campo editable).
-- Saldo "natural": ASSET/EXPENSE crecen al debe; LIABILITY/EQUITY/INCOME al haber.
-- -----------------------------------------------------------------------------
CREATE VIEW account_balances AS
SELECT
    a.id AS account_id,
    a.code,
    a.type,
    a.owner,
    a.owner_id,
    a.currency,
    a.status,
    -- SUM() sobre bigint devuelve NUMERIC en Postgres; el saldo es bigint por definición
    -- (unidades menores), así que se castea explícitamente en vez de coercionarlo al leer.
    CASE WHEN a.type IN ('ASSET', 'EXPENSE')
         THEN COALESCE(SUM(CASE WHEN e.direction = 'DEBIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)
         ELSE COALESCE(SUM(CASE WHEN e.direction = 'CREDIT' THEN e.amount_minor ELSE -e.amount_minor END), 0)
    END::BIGINT AS balance_minor
FROM accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id
GROUP BY a.id;

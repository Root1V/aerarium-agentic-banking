-- =============================================================================
-- AIBank core — V012: bolsillos
--
-- Un bolsillo es dinero apartado dentro de la cuenta («para la tarjeta»,
-- «vacaciones»). Es una cuenta hija: del mismo cliente, en la misma moneda y bajo
-- el mismo producto que su cuenta madre, y de un solo nivel. Cada una tiene su
-- saldo, así que lo apartado no se gasta sin querer.
--
-- Dos reglas hacen que un bolsillo no sea una forma de saltarse los topes:
--   (1) El tope de SALDO del producto se aplica a la familia —la cuenta y sus
--       bolsillos juntos—. Abrir bolsillos no permite guardar más de lo que la
--       cuenta de nivel 1 admite.
--   (2) Mover dinero entre la cuenta y sus bolsillos (`pocket_transfer`) no cuenta
--       contra el tope POR OPERACIÓN: el dinero no entra ni sale del cliente. Por
--       eso un `pocket_transfer` solo puede tocar cuentas de una misma familia;
--       uno que toque la cuenta de otro se rechaza (AB013).
-- =============================================================================

ALTER TABLE accounts ADD COLUMN parent_account_id UUID REFERENCES accounts (id);
CREATE INDEX idx_accounts_parent ON accounts (parent_account_id) WHERE parent_account_id IS NOT NULL;

-- Un bolsillo hereda todo de su madre, y no hay bolsillos de bolsillos.
CREATE FUNCTION check_pocket_parent() RETURNS trigger AS $$
DECLARE
    parent RECORD;
BEGIN
    IF NEW.parent_account_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT owner, owner_id, currency, product_id, status, parent_account_id
      INTO parent FROM accounts WHERE id = NEW.parent_account_id;
    IF NOT FOUND
       OR parent.owner <> 'CUSTOMER' OR NEW.owner <> 'CUSTOMER'
       OR parent.owner_id IS DISTINCT FROM NEW.owner_id
       OR parent.currency <> NEW.currency
       OR parent.product_id IS DISTINCT FROM NEW.product_id THEN
        RAISE EXCEPTION 'pocket % must share customer, currency and product with its account', NEW.id
            USING ERRCODE = 'AB013';
    END IF;
    IF parent.parent_account_id IS NOT NULL THEN
        RAISE EXCEPTION 'a pocket cannot have pockets' USING ERRCODE = 'AB013';
    END IF;
    IF parent.status <> 'ACTIVE' THEN
        RAISE EXCEPTION 'account % is not active', NEW.parent_account_id USING ERRCODE = 'AB013';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_accounts_pocket_parent
    BEFORE INSERT OR UPDATE OF parent_account_id ON accounts
    FOR EACH ROW EXECUTE FUNCTION check_pocket_parent();

-- -----------------------------------------------------------------------------
-- Límites: la de la V011, con los bolsillos.
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION check_account_limits() RETURNS trigger AS $$
DECLARE
    acc RECORD;
    bal BIGINT;
    family_bal BIGINT;
    prod RECORD;
    line RECORD;
    tx_kind TEXT;
    families INT;
BEGIN
    SELECT allows_overdraft, product_id, COALESCE(parent_account_id, id) AS family
      INTO acc FROM accounts WHERE id = NEW.account_id;
    SELECT balance_micros INTO bal FROM account_balances WHERE account_id = NEW.account_id;
    SELECT kind INTO tx_kind FROM ledger_transactions WHERE id = NEW.transaction_id;

    SELECT limit_micros, status INTO line FROM credit_lines WHERE account_id = NEW.account_id;
    IF FOUND THEN
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

    -- Un movimiento entre bolsillos se queda dentro de una familia.
    IF tx_kind = 'pocket_transfer' THEN
        SELECT count(DISTINCT COALESCE(a.parent_account_id, a.id)) INTO families
          FROM ledger_entries e JOIN accounts a ON a.id = e.account_id
         WHERE e.transaction_id = NEW.transaction_id;
        IF families <> 1 THEN
            RAISE EXCEPTION 'pocket transfer % moves money outside one account and its pockets',
                NEW.transaction_id
                USING ERRCODE = 'AB013';
        END IF;
    END IF;

    IF acc.product_id IS NOT NULL THEN
        SELECT max_balance_micros, max_transaction_micros INTO prod
        FROM products WHERE id = acc.product_id;

        -- El tope de saldo es de la cuenta con sus bolsillos.
        SELECT COALESCE(sum(b.balance_micros), 0) INTO family_bal
          FROM accounts a JOIN account_balances b ON b.account_id = a.id
         WHERE a.id = acc.family OR a.parent_account_id = acc.family;
        IF prod.max_balance_micros IS NOT NULL AND family_bal > prod.max_balance_micros THEN
            RAISE EXCEPTION 'balance cap exceeded: account % and its pockets would end at %, cap %',
                NEW.account_id, family_bal, prod.max_balance_micros
                USING ERRCODE = 'AB002';
        END IF;

        IF prod.max_transaction_micros IS NOT NULL
           AND tx_kind IS DISTINCT FROM 'pocket_transfer'
           AND NEW.amount_micros > prod.max_transaction_micros THEN
            RAISE EXCEPTION 'transaction cap exceeded: account % amount %, cap %',
                NEW.account_id, NEW.amount_micros, prod.max_transaction_micros
                USING ERRCODE = 'AB003';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

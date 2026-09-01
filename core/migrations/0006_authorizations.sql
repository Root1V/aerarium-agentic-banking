-- =============================================================================
-- Autorizaciones — V006
--
-- Generaliza el patrón retención → captura que ya vivía en el adaptador de
-- tarjetas. Vive en el CORE y no en un adaptador porque el pagador y el receptor
-- son dos cuentas del banco: la liquidación es una sola transacción de ledger y
-- tiene que confirmarse junto con el cambio de estado, no después.
--
-- El dinero de una autorización viva está en una cuenta de retención (PASIVO):
-- salió del saldo disponible del pagador pero sigue siendo del cliente hasta que
-- se capture. Los cuatro movimientos posibles son siempre de dos asientos:
--
--   autorizar : debe pagador     / haber retención
--   capturar  : debe retención   / haber receptor
--   liberar   : debe retención   / haber pagador      (vencimiento)
--   reembolsar: debe receptor    / haber pagador
-- =============================================================================

CREATE TYPE authorization_status AS ENUM (
    'AUTHORIZED',
    'CAPTURED',
    'PARTIALLY_REFUNDED',
    'REFUNDED',
    'RELEASED'
);

CREATE TABLE authorizations (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- La define quien origina el pago. Garantiza que un reintento de red no cree
    -- una segunda autorización por el mismo cobro.
    idempotency_key       TEXT NOT NULL UNIQUE,
    request_hash          TEXT NOT NULL,

    payer_account_id      UUID NOT NULL REFERENCES accounts (id),
    payee_account_id      UUID NOT NULL REFERENCES accounts (id),
    amount_micros         BIGINT NOT NULL CHECK (amount_micros > 0),
    currency              CHAR(3) NOT NULL,

    status                authorization_status NOT NULL DEFAULT 'AUTHORIZED',

    -- Cuánto se devolvió ya. El modelo admite reembolso parcial desde el día uno
    -- aunque la API exponga primero solo el total: agregarlo después, con datos
    -- vivos, obliga a reinterpretar filas existentes.
    refunded_micros       BIGINT NOT NULL DEFAULT 0 CHECK (refunded_micros >= 0),

    hold_transaction_id   UUID NOT NULL REFERENCES ledger_transactions (id),
    settle_transaction_id UUID REFERENCES ledger_transactions (id),

    -- Vencimiento. Sin liberación automática, una autorización sin capturar
    -- inmoviliza el dinero del pagador para siempre.
    expires_at            TIMESTAMPTZ NOT NULL,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at            TIMESTAMPTZ,
    released_at           TIMESTAMPTZ,

    CONSTRAINT payer_is_not_payee CHECK (payer_account_id <> payee_account_id),
    CONSTRAINT refund_within_captured CHECK (refunded_micros <= amount_micros)
);

CREATE INDEX idx_authorizations_payer ON authorizations (payer_account_id, created_at DESC);
CREATE INDEX idx_authorizations_payee ON authorizations (payee_account_id, created_at DESC);

-- Cola del barrendero de vencidas: índice parcial, solo sobre las vivas.
CREATE INDEX idx_authorizations_expiring
    ON authorizations (expires_at)
    WHERE status = 'AUTHORIZED';

-- -----------------------------------------------------------------------------
-- Lo que identifica a la autorización es inmutable.
--
-- Cambiar el monto o las cuentas después de haber retenido el dinero dejaría el
-- registro contradiciendo a los asientos que ya existen, y los asientos no se
-- pueden corregir: el ledger es append-only.
-- -----------------------------------------------------------------------------
CREATE FUNCTION authorizations_forbid_identity_change() RETURNS trigger AS $$
BEGIN
    IF NEW.payer_account_id  IS DISTINCT FROM OLD.payer_account_id
       OR NEW.payee_account_id IS DISTINCT FROM OLD.payee_account_id
       OR NEW.amount_micros    IS DISTINCT FROM OLD.amount_micros
       OR NEW.currency         IS DISTINCT FROM OLD.currency
       OR NEW.idempotency_key  IS DISTINCT FROM OLD.idempotency_key
       OR NEW.hold_transaction_id IS DISTINCT FROM OLD.hold_transaction_id
    THEN
        RAISE EXCEPTION 'authorization % is immutable in amount, accounts and hold', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_authorizations_immutable
    BEFORE UPDATE ON authorizations
    FOR EACH ROW EXECUTE FUNCTION authorizations_forbid_identity_change();

-- -----------------------------------------------------------------------------
-- Transiciones permitidas.
--
-- Está en la base y no solo en el servicio a propósito: es la regla que impide
-- gastar dos veces la misma retención. Un error en cualquier capa de arriba
-- —o un segundo proceso que no conozca la máquina de estados— se estrella aquí
-- en vez de mover dinero dos veces.
--
--   AUTHORIZED          -> CAPTURED | RELEASED
--   CAPTURED            -> PARTIALLY_REFUNDED | REFUNDED
--   PARTIALLY_REFUNDED  -> PARTIALLY_REFUNDED | REFUNDED
--   REFUNDED, RELEASED  -> terminales
-- -----------------------------------------------------------------------------
CREATE FUNCTION authorizations_check_transition() RETURNS trigger AS $$
BEGIN
    IF NEW.status = OLD.status AND NEW.status <> 'PARTIALLY_REFUNDED' THEN
        RETURN NEW;
    END IF;

    IF NOT (
        (OLD.status = 'AUTHORIZED'         AND NEW.status IN ('CAPTURED', 'RELEASED'))
     OR (OLD.status = 'CAPTURED'           AND NEW.status IN ('PARTIALLY_REFUNDED', 'REFUNDED'))
     OR (OLD.status = 'PARTIALLY_REFUNDED' AND NEW.status IN ('PARTIALLY_REFUNDED', 'REFUNDED'))
    ) THEN
        RAISE EXCEPTION 'authorization % cannot go from % to %', OLD.id, OLD.status, NEW.status
            USING ERRCODE = 'AB004';
    END IF;

    -- Un reembolso solo puede crecer, nunca deshacerse.
    IF NEW.refunded_micros < OLD.refunded_micros THEN
        RAISE EXCEPTION 'authorization % cannot un-refund', OLD.id USING ERRCODE = 'AB004';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_authorizations_transition
    BEFORE UPDATE ON authorizations
    FOR EACH ROW EXECUTE FUNCTION authorizations_check_transition();

-- -----------------------------------------------------------------------------
-- Reembolsos: uno por devolución, no un campo en la autorización.
--
-- Un reembolso total es un caso de uno parcial, no al revés. Con una tabla hija
-- desde el principio, exponer el parcial más adelante es agregar un parámetro a
-- la API; con un campo, sería migrar filas que ya representan dinero devuelto.
-- -----------------------------------------------------------------------------
CREATE TABLE authorization_refunds (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    authorization_id UUID NOT NULL REFERENCES authorizations (id),
    idempotency_key  TEXT NOT NULL UNIQUE,
    amount_micros    BIGINT NOT NULL CHECK (amount_micros > 0),
    transaction_id   UUID NOT NULL REFERENCES ledger_transactions (id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_authorization_refunds_auth ON authorization_refunds (authorization_id);

CREATE TRIGGER trg_authorization_refunds_append_only
    BEFORE UPDATE OR DELETE ON authorization_refunds
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();

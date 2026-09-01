-- Estado del adaptador de tarjetas.
--
-- Una autorización vive días entre que se retiene el dinero y que el comercio
-- cobra, así que no puede guardarse en memoria. Y el monto autorizado se conserva
-- AQUÍ, no se toma de lo que informe la red al cobrar: el emisor decide contra su
-- propio registro, no contra el dato de un tercero.

CREATE SCHEMA IF NOT EXISTS cards;

CREATE TABLE IF NOT EXISTS cards.authorizations (
    -- Identidad de la compra en la red: enlaza autorización, cobro y reversa.
    network_transaction_id TEXT PRIMARY KEY,
    card_id                TEXT NOT NULL,
    account_id             UUID NOT NULL,
    amount_micros          BIGINT NOT NULL CHECK (amount_micros > 0),
    currency               CHAR(3) NOT NULL,
    merchant_name          TEXT,
    -- held: dinero retenido · cleared: cobrado · reversed: liberado sin cobro
    status                 TEXT NOT NULL,
    hold_transaction_id    UUID,
    -- Monto realmente cobrado; puede diferir del autorizado.
    cleared_amount_micros  BIGINT,
    -- Excedente que el cliente no pudo cubrir y el banco adelantó.
    overage_micros         BIGINT NOT NULL DEFAULT 0,
    authorized_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at            TIMESTAMPTZ
);

-- Las retenciones vivas son la cola operativa: expiran, se concilian, se vigilan.
CREATE INDEX IF NOT EXISTS idx_authorizations_held
    ON cards.authorizations (authorized_at) WHERE status = 'held';

-- Los adelantos por excedente son cartera a recuperar.
CREATE INDEX IF NOT EXISTS idx_authorizations_overage
    ON cards.authorizations (account_id) WHERE overage_micros > 0;

-- Constancias verificables. En el Perú son comunes las estafas con capturas
-- falsas de un pago: quien recibe no tiene cómo comprobar que es real. Una
-- constancia de Aerarium lleva un código que cualquiera —cliente o no— puede
-- comprobar con el banco.
--
-- Se guarda una foto de lo que dice la constancia, no un puntero: lo que se
-- verifica es exactamente lo que se mostró. Los nombres van acortados.

CREATE SCHEMA IF NOT EXISTS receipts;

CREATE TABLE IF NOT EXISTS receipts.receipts (
    code           TEXT PRIMARY KEY,
    transaction_id UUID NOT NULL,
    account_id     UUID NOT NULL,
    customer_id    UUID NOT NULL,
    kind           TEXT NOT NULL,
    amount_micros  BIGINT NOT NULL CHECK (amount_micros > 0),
    currency       CHAR(3) NOT NULL,
    from_name      TEXT NOT NULL,
    to_name        TEXT NOT NULL,
    posted_at      TIMESTAMPTZ NOT NULL,
    issued_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Una constancia por movimiento y cuenta: pedirla otra vez da la misma.
    UNIQUE (transaction_id, account_id)
);

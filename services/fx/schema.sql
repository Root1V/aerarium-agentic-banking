-- Cotizaciones de cambio entre cuentas propias (F3-09).
--
-- Una cotización congela el tipo de cambio unos segundos: lo que la persona ve es
-- exactamente lo que se ejecuta. Se guarda el tipo de referencia junto al del
-- banco, para poder decirle cuánto le costó el cambio.

CREATE SCHEMA IF NOT EXISTS fx;

CREATE TABLE IF NOT EXISTS fx.quotes (
    id               UUID PRIMARY KEY,
    customer_id      UUID NOT NULL,
    from_account_id  UUID NOT NULL,
    to_account_id    UUID NOT NULL,
    from_currency    CHAR(3) NOT NULL,
    to_currency      CHAR(3) NOT NULL,
    from_micros      BIGINT NOT NULL CHECK (from_micros > 0),
    to_micros        BIGINT NOT NULL CHECK (to_micros > 0),
    -- Soles por dólar, en millonésimas: 3,38 es 3380000.
    reference_rate   BIGINT NOT NULL,
    applied_rate     BIGINT NOT NULL,
    -- Lo que la persona deja de recibir frente al tipo de referencia, en la moneda
    -- que recibe: el costo del cambio, dicho.
    cost_micros      BIGINT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,
    transaction_id   UUID
);

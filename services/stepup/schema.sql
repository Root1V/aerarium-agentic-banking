-- Destinatarios a los que un titular ya envió dinero con una operación confirmada.
--
-- Enviarle a alguien por primera vez es el momento más explotado por los
-- estafadores ("transfiere a esta cuenta segura"): por eso pide confirmar con la
-- cara aunque el monto sea pequeño. Tras la primera confirmación, ese
-- destinatario queda conocido y los envíos chicos ya no la piden.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE IF NOT EXISTS identity.known_payees (
    customer_id  UUID NOT NULL,
    account_id   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (customer_id, account_id)
);

-- Cuándo se le envió por última vez: ordena la lista de destinatarios recientes.
ALTER TABLE identity.known_payees ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE INDEX IF NOT EXISTS idx_known_payees_recent ON identity.known_payees (customer_id, last_used_at DESC);

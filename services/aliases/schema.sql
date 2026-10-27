-- Directorio de celulares: a qué cuenta llega lo que se envía a un número.
--
-- Un número por cliente y un cliente por número. Si un número verificado ya
-- estaba a nombre de otro, pasa al que acaba de verificarlo: los operadores
-- reasignan números, y quien hoy recibe los SMS es quien hoy lo tiene.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE IF NOT EXISTS identity.phone_aliases (
    -- E.164: +51 y nueve dígitos.
    phone        TEXT PRIMARY KEY,
    customer_id  UUID NOT NULL,
    account_id   TEXT NOT NULL,
    verified_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_phone_aliases_customer ON identity.phone_aliases (customer_id);

-- Códigos enviados por SMS y todavía sin usar.
CREATE TABLE IF NOT EXISTS identity.phone_verifications (
    id           UUID PRIMARY KEY,
    customer_id  UUID NOT NULL,
    account_id   TEXT NOT NULL,
    phone        TEXT NOT NULL,
    -- SHA-256 del id y el código: seis dígitos se adivinan con un millón de
    -- intentos; lo que protege es el tope de intentos, no el hash.
    code_hash    BYTEA NOT NULL,
    attempts     INT NOT NULL DEFAULT 0,
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ
);

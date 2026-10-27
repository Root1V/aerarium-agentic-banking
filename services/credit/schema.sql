-- Solicitudes de crédito y su decisión.
--
-- Cada solicitud guarda el expediente COMPLETO que devolvió el motor de riesgo:
-- qué se observó, con qué modelo y por qué salió lo que salió. Ante un reclamo o
-- una revisión del supervisor, esto es lo que se muestra. Nunca se borra ni se
-- reescribe una decisión: una nueva solicitud es una fila nueva.

CREATE SCHEMA IF NOT EXISTS credit;

CREATE TABLE IF NOT EXISTS credit.applications (
    id                 UUID PRIMARY KEY,
    customer_id        UUID NOT NULL,
    -- La cuenta cuyos movimientos se evaluaron.
    account_id         TEXT NOT NULL,
    outcome            TEXT NOT NULL CHECK (outcome IN ('approved', 'needs_more_history', 'declined')),
    limit_micros       BIGINT NOT NULL,
    currency           TEXT NOT NULL,
    score              INT NOT NULL,
    model_version      TEXT NOT NULL,
    reasons            JSONB NOT NULL,
    -- El expediente tal cual lo devolvió el motor.
    record             JSONB NOT NULL,
    -- Solo en el sandbox: el historial evaluado incluyó movimientos sintéticos,
    -- porque una cuenta recién abierta nunca califica. Queda dicho en la decisión.
    synthetic_history  BOOLEAN NOT NULL DEFAULT FALSE,
    credit_line_id     TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_credit_applications_customer
    ON credit.applications (customer_id, created_at DESC);

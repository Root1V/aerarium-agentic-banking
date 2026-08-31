-- Esquema del servicio de onboarding.
--
-- Vive en su propio schema, separado del core: son servicios distintos y el core
-- no debe saber nada de expedientes de alta ni de proveedores de KYC.
--
-- El expediente y sus pasos son el RASTRO DE AUDITORÍA que exige el supervisor:
-- ante una consulta, debe poder reconstruirse por qué se aprobó o rechazó a cada
-- persona, con qué proveedor y cuándo.

CREATE SCHEMA IF NOT EXISTS onboarding;

CREATE TABLE IF NOT EXISTS onboarding.applications (
    id               UUID PRIMARY KEY,
    -- Referencia externa del canal (app móvil, sucursal). Única: reenviar la misma
    -- solicitud devuelve el expediente existente en vez de abrir uno nuevo.
    external_ref     TEXT NOT NULL UNIQUE,
    customer_id      UUID NOT NULL,
    product_code     TEXT NOT NULL,
    status           TEXT NOT NULL,
    full_name        TEXT NOT NULL,
    document_type    TEXT NOT NULL,
    document_number  TEXT NOT NULL,
    date_of_birth    DATE NOT NULL,
    country_code     CHAR(2) NOT NULL,
    account_id       UUID,
    decision_reason  TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_applications_status ON onboarding.applications (status);

-- Un paso por verificación. Se conserva el resultado para no repetir trabajo ya
-- pagado al proveedor y para sustentar la decisión ante el supervisor.
CREATE TABLE IF NOT EXISTS onboarding.application_steps (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    application_id UUID NOT NULL REFERENCES onboarding.applications (id),
    step           TEXT NOT NULL,
    outcome        TEXT NOT NULL,
    provider       TEXT,
    provider_ref   TEXT,
    detail         TEXT,
    completed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (application_id, step)
);

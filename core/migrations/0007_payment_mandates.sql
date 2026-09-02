-- =============================================================================
-- Mandatos de pago — V007
--
-- Un mandato es el permiso que un TITULAR le da a una integración para iniciar
-- pagos desde una cuenta suya. Es lo que hace posible el "modelo B": el cliente
-- es cliente del banco, el dinero nunca sale a un pool de un tercero, y la
-- plataforma que orquesta el pago solo tiene el permiso que el titular le dio.
--
-- # Por qué es una entidad y no un scope del token
--
-- Un scope dice qué puede hacer una integración. Un mandato dice qué autorizó
-- UNA PERSONA, sobre QUÉ cuenta, hasta QUÉ monto y hasta CUÁNDO. En una disputa
-- —"yo nunca autoricé ese pago"— un scope no prueba nada; esta fila sí, porque
-- registra quién se autenticó y contra qué evidencia de consentimiento.
--
-- Perú no tiene marco de iniciación de pagos (sin open finance obligatorio), así
-- que no hay un régimen que reparta la responsabilidad. Este registro es lo
-- único que la reparte.
-- =============================================================================

CREATE TYPE mandate_status AS ENUM ('ACTIVE', 'REVOKED');

CREATE TABLE payment_mandates (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Cuenta sobre la que se puede iniciar el pago. Debe ser una sub-cuenta de
    -- propósito del titular, no su cuenta principal: un agente con un error no
    -- puede tener alcance sobre todo el dinero de una persona.
    account_id        UUID NOT NULL REFERENCES accounts (id),

    -- A quién se le concede: el client_id de la integración.
    grantee           TEXT NOT NULL,

    -- Quién lo concedió. Es el titular que se autenticó contra el banco.
    granted_by        UUID NOT NULL,
    -- Evidencia del flujo de consentimiento: qué sesión lo autorizó y cómo.
    consent_reference TEXT NOT NULL,

    currency          CHAR(3) NOT NULL,

    -- Topes. NULL = sin tope por esa dimensión.
    max_per_operation_micros BIGINT CHECK (max_per_operation_micros IS NULL OR max_per_operation_micros > 0),
    max_total_micros         BIGINT CHECK (max_total_micros IS NULL OR max_total_micros > 0),

    -- No hay columna de "consumido" A PROPÓSITO. Lo consumido se DERIVA de las
    -- autorizaciones vivas del mandato (ver `mandate_consumption`): un contador
    -- que hay que subir al autorizar y bajar al liberar y al reembolsar es un
    -- contador que termina desincronizado, y aquí desincronizarse significa o
    -- bloquear pagos legítimos o permitir los que superan el tope.

    status            mandate_status NOT NULL DEFAULT 'ACTIVE',
    expires_at        TIMESTAMPTZ NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    -- Quién revocó: el titular, una operación interna o la propia integración.
    revoked_by        TEXT,

    CONSTRAINT revoked_has_timestamp
        CHECK ((status = 'REVOKED') = (revoked_at IS NOT NULL))
);

-- La consulta caliente: "¿tiene esta integración permiso vivo sobre esta cuenta?"
CREATE INDEX idx_mandates_lookup
    ON payment_mandates (account_id, grantee)
    WHERE status = 'ACTIVE';

-- Para que el titular vea sus mandatos en la app.
CREATE INDEX idx_mandates_granted_by ON payment_mandates (granted_by, created_at DESC);

-- -----------------------------------------------------------------------------
-- Lo que identifica al mandato es inmutable.
--
-- Cambiar la cuenta, el beneficiario o el tope después de otorgado convertiría
-- el registro en algo distinto de lo que la persona aprobó en pantalla — y es
-- justo ese registro el que se presenta en una disputa. Para cambiar un permiso
-- se revoca y se otorga otro.
-- -----------------------------------------------------------------------------
CREATE FUNCTION mandates_forbid_identity_change() RETURNS trigger AS $$
BEGIN
    IF NEW.account_id  IS DISTINCT FROM OLD.account_id
       OR NEW.grantee    IS DISTINCT FROM OLD.grantee
       OR NEW.granted_by IS DISTINCT FROM OLD.granted_by
       OR NEW.currency   IS DISTINCT FROM OLD.currency
       OR NEW.max_per_operation_micros IS DISTINCT FROM OLD.max_per_operation_micros
       OR NEW.max_total_micros         IS DISTINCT FROM OLD.max_total_micros
       OR NEW.expires_at IS DISTINCT FROM OLD.expires_at
       OR NEW.consent_reference IS DISTINCT FROM OLD.consent_reference
    THEN
        RAISE EXCEPTION 'mandate % is immutable: revoke it and grant a new one', OLD.id
            USING ERRCODE = 'AB005';
    END IF;

    -- Un mandato revocado es terminal. Reactivarlo resucitaría un permiso que la
    -- persona retiró.
    IF OLD.status = 'REVOKED' AND NEW.status = 'ACTIVE' THEN
        RAISE EXCEPTION 'mandate % was revoked and cannot be reactivated', OLD.id
            USING ERRCODE = 'AB005';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_mandates_immutable
    BEFORE UPDATE ON payment_mandates
    FOR EACH ROW EXECUTE FUNCTION mandates_forbid_identity_change();

-- -----------------------------------------------------------------------------
-- Enlace entre la autorización y el mandato que la habilitó.
--
-- Separado de `authorizations` porque no toda autorización nace de un mandato:
-- en el modelo ómnibus la integración opera sobre sus propias sub-cuentas y no
-- hay titular externo que haya consentido nada. Una columna anulable en
-- `authorizations` diría "aquí falta algo" en la mitad de las filas; esta tabla
-- dice lo que es: las autorizaciones iniciadas por delegación.
-- -----------------------------------------------------------------------------
CREATE TABLE authorization_mandates (
    authorization_id UUID PRIMARY KEY REFERENCES authorizations (id),
    mandate_id       UUID NOT NULL REFERENCES payment_mandates (id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_authorization_mandates_mandate ON authorization_mandates (mandate_id);

CREATE TRIGGER trg_authorization_mandates_append_only
    BEFORE UPDATE OR DELETE ON authorization_mandates
    FOR EACH ROW EXECUTE FUNCTION ledger_forbid_mutation();

-- -----------------------------------------------------------------------------
-- Cuánto lleva consumido cada mandato, derivado de sus autorizaciones.
--
-- Cuenta lo que está comprometido de verdad: una retención viva compromete
-- dinero aunque todavía no se haya capturado, y contar solo lo capturado dejaría
-- a un agente autorizando muchas veces por encima del tope mientras nada se
-- captura. Lo liberado y lo reembolsado no cuentan, porque el dinero volvió.
-- -----------------------------------------------------------------------------
CREATE VIEW mandate_consumption AS
SELECT
    m.id AS mandate_id,
    COALESCE(SUM(
        CASE a.status
            WHEN 'RELEASED' THEN 0
            ELSE a.amount_micros - a.refunded_micros
        END
    ), 0)::BIGINT AS consumed_micros
FROM payment_mandates m
LEFT JOIN authorization_mandates am ON am.mandate_id = m.id
LEFT JOIN authorizations a ON a.id = am.authorization_id
GROUP BY m.id;

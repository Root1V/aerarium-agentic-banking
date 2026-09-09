//! Mandatos de pago: el permiso que un titular le da a una plataforma.
//!
//! Es la pieza del **modelo B**. En el modelo ómnibus la plataforma opera sobre
//! sub-cuentas suyas y le basta con demostrar que es ella. Aquí opera sobre la
//! cuenta de un cliente del banco, y demostrar que es ella **no alcanza**: hace
//! falta demostrar que esa persona la autorizó.
//!
//! # Un mandato no es un scope
//!
//! Un scope dice qué puede hacer una integración. Un mandato dice qué autorizó
//! una persona concreta, sobre qué cuenta, hasta qué monto y hasta cuándo, y
//! deja constancia de cómo se autenticó.
//!
//! La diferencia se ve en una disputa. Ante "yo nunca autoricé ese pago", un
//! scope no prueba nada: solo dice que la plataforma tenía permiso genérico. El
//! mandato sí, porque nació de un flujo donde el titular se autenticó **contra
//! el banco** y vio en pantalla lo que estaba aprobando.
//!
//! Perú no tiene marco de iniciación de pagos —no hay open finance obligatorio—,
//! así que no existe un régimen que reparta la responsabilidad entre banco e
//! iniciador. Este registro es lo único que la reparte, y por eso su diseño no
//! es burocracia.
//!
//! # Qué NO hace la revocación
//!
//! Revocar corta la posibilidad de iniciar pagos NUEVOS. **No cancela las
//! retenciones vivas**, porque del otro lado puede haber un vendedor que ya
//! entregó lo que se le pagó. La exposición está acotada por la vigencia de la
//! retención —quince minutos— y esa es una elección deliberada: dejar sin cobro
//! a quien ya cumplió sería trasladarle a él un problema que no es suyo.

use crate::authorizations::{
    authorize_in_tx, AuthorizationError, AuthorizeCommand, AuthorizeResult,
};
use crate::model::AccountStatus;
use chrono::{DateTime, Utc};
use sqlx::{PgPool, Postgres, Transaction};
use uuid::Uuid;

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "mandate_status", rename_all = "UPPERCASE")]
pub enum MandateStatus {
    Active,
    Revoked,
}

/// Estado observable: se deriva del almacenado y del reloj, igual que en las
/// autorizaciones. `Expired` no se guarda porque guardarlo obligaría a escribir
/// en la base cada vez que pasa el tiempo.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ObservedMandateStatus {
    Active,
    Revoked,
    Expired,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Mandate {
    pub id: Uuid,
    /// Cuenta sobre la que se puede iniciar el pago.
    pub account_id: Uuid,
    /// `client_id` de la integración autorizada.
    pub grantee: String,
    /// Titular que otorgó el permiso.
    pub granted_by: Uuid,
    /// Evidencia del flujo de consentimiento.
    pub consent_reference: String,
    pub currency: String,
    pub max_per_operation_micros: Option<i64>,
    pub max_total_micros: Option<i64>,
    pub status: MandateStatus,
    pub expires_at: DateTime<Utc>,
    pub created_at: DateTime<Utc>,
    pub revoked_at: Option<DateTime<Utc>>,
    pub revoked_by: Option<String>,
}

impl Mandate {
    pub fn observed_at(&self, now: DateTime<Utc>) -> ObservedMandateStatus {
        match self.status {
            MandateStatus::Revoked => ObservedMandateStatus::Revoked,
            MandateStatus::Active if self.expires_at <= now => ObservedMandateStatus::Expired,
            MandateStatus::Active => ObservedMandateStatus::Active,
        }
    }

    pub fn observed(&self) -> ObservedMandateStatus {
        self.observed_at(Utc::now())
    }

    pub fn is_usable(&self) -> bool {
        self.observed() == ObservedMandateStatus::Active
    }
}

/// Mandato junto a cuánto lleva comprometido.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MandateWithUsage {
    pub mandate: Mandate,
    /// Derivado de las autorizaciones vivas, nunca de un contador guardado.
    pub consumed_micros: i64,
}

impl MandateWithUsage {
    /// Cuánto queda por gastar. `None` si el mandato no tiene tope total.
    pub fn remaining_micros(&self) -> Option<i64> {
        self.mandate
            .max_total_micros
            .map(|total| (total - self.consumed_micros).max(0))
    }
}

#[derive(Debug, thiserror::Error)]
pub enum MandateError {
    #[error("invalid request: {0}")]
    Invalid(String),

    #[error("mandate {0} not found")]
    NotFound(Uuid),

    #[error("account {0} not found")]
    AccountNotFound(Uuid),

    /// El titular retiró el permiso.
    #[error("mandate {0} was revoked")]
    Revoked(Uuid),

    /// La vigencia terminó.
    #[error("mandate {0} expired at {1}")]
    Expired(Uuid, DateTime<Utc>),

    /// El pago cabe en el saldo pero no en lo que el titular autorizó.
    #[error("{0}")]
    LimitExceeded(String),

    /// El mandato no habilita esa cuenta como pagadora.
    #[error("mandate {0} does not cover account {1}")]
    AccountNotCovered(Uuid, Uuid),

    #[error(transparent)]
    Authorization(#[from] AuthorizationError),

    #[error(transparent)]
    Database(#[from] sqlx::Error),
}

/// Orden de otorgamiento. La construye el flujo de consentimiento, nunca la
/// integración por su cuenta.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GrantCommand {
    pub account_id: Uuid,
    pub grantee: String,
    pub granted_by: Uuid,
    pub consent_reference: String,
    pub max_per_operation_micros: Option<i64>,
    pub max_total_micros: Option<i64>,
    pub expires_at: DateTime<Utc>,
}

pub struct MandateService {
    pool: PgPool,
}

impl MandateService {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Registra el permiso que un titular otorgó.
    pub async fn grant(&self, command: &GrantCommand) -> Result<Mandate, MandateError> {
        validate_grant(command)?;

        let mut tx = self.pool.begin().await?;

        let account = sqlx::query!(
            r#"SELECT id, currency, owner_id, status AS "status: AccountStatus"
                 FROM accounts WHERE id = $1"#,
            command.account_id,
        )
        .fetch_optional(&mut *tx)
        .await?
        .ok_or(MandateError::AccountNotFound(command.account_id))?;

        if account.status != AccountStatus::Active {
            return Err(MandateError::Invalid(format!(
                "account {} is not active",
                command.account_id
            )));
        }

        // Solo el titular puede delegar sobre su propia cuenta. Sin esta
        // comprobación, un flujo de consentimiento con un fallo podría otorgar
        // permiso sobre la cuenta de otra persona.
        if account.owner_id != Some(command.granted_by) {
            return Err(MandateError::Invalid(
                "only the account holder can grant a mandate over their account".into(),
            ));
        }

        let row = sqlx::query!(
            r#"
            INSERT INTO payment_mandates (
                account_id, grantee, granted_by, consent_reference, currency,
                max_per_operation_micros, max_total_micros, expires_at
            )
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
            RETURNING id, created_at
            "#,
            command.account_id,
            command.grantee,
            command.granted_by,
            command.consent_reference,
            account.currency,
            command.max_per_operation_micros,
            command.max_total_micros,
            command.expires_at,
        )
        .fetch_one(&mut *tx)
        .await?;

        tx.commit().await?;

        Ok(Mandate {
            id: row.id,
            account_id: command.account_id,
            grantee: command.grantee.clone(),
            granted_by: command.granted_by,
            consent_reference: command.consent_reference.clone(),
            currency: account.currency,
            max_per_operation_micros: command.max_per_operation_micros,
            max_total_micros: command.max_total_micros,
            status: MandateStatus::Active,
            expires_at: command.expires_at,
            created_at: row.created_at,
            revoked_at: None,
            revoked_by: None,
        })
    }

    /// Retira el permiso. Idempotente: revocar dos veces no es un error.
    pub async fn revoke(&self, id: Uuid, revoked_by: &str) -> Result<Mandate, MandateError> {
        let mut tx = self.pool.begin().await?;
        let mandate = lock_mandate(&mut tx, id).await?;

        if mandate.status == MandateStatus::Revoked {
            return Ok(mandate);
        }

        let now = Utc::now();
        sqlx::query!(
            "UPDATE payment_mandates SET status = 'REVOKED', revoked_at = $2, revoked_by = $3 WHERE id = $1",
            id,
            now,
            revoked_by,
        )
        .execute(&mut *tx)
        .await?;

        tx.commit().await?;

        Ok(Mandate {
            status: MandateStatus::Revoked,
            revoked_at: Some(now),
            revoked_by: Some(revoked_by.to_owned()),
            ..mandate
        })
    }

    /// Inicia un pago bajo un mandato.
    ///
    /// Comprobar el permiso y retener el dinero ocurren en **la misma
    /// transacción**, con la fila del mandato bloqueada. Si se hicieran por
    /// separado, entre la comprobación y la retención cabría una revocación —o
    /// una segunda autorización concurrente— y el tope dejaría de significar
    /// nada.
    pub async fn authorize(
        &self,
        mandate_id: Uuid,
        command: &AuthorizeCommand,
    ) -> Result<(AuthorizeResult, MandateWithUsage), MandateError> {
        let mut tx = self.pool.begin().await?;

        let mandate = lock_mandate(&mut tx, mandate_id).await?;
        let consumed = consumption_of(&mut tx, mandate_id).await?;

        check_usable(&mandate)?;

        if mandate.account_id != command.payer_account_id {
            return Err(MandateError::AccountNotCovered(
                mandate_id,
                command.payer_account_id,
            ));
        }
        if mandate.currency != command.currency {
            return Err(MandateError::Invalid(format!(
                "mandate is in {}, payment requested in {}",
                mandate.currency, command.currency
            )));
        }

        if mandate
            .max_per_operation_micros
            .is_some_and(|max| command.amount_micros > max)
        {
            return Err(MandateError::LimitExceeded(format!(
                "payment of {} exceeds the per-operation limit of {}",
                command.amount_micros,
                mandate.max_per_operation_micros.unwrap_or_default()
            )));
        }
        if let Some(total) = mandate.max_total_micros {
            let remaining = total - consumed;
            if command.amount_micros > remaining {
                return Err(MandateError::LimitExceeded(format!(
                    "payment of {} exceeds the {} still available on this mandate",
                    command.amount_micros, remaining.max(0)
                )));
            }
        }

        let result = authorize_in_tx(&mut tx, command).await?;

        // El enlace se registra siempre, también en un replay: la clave de
        // idempotencia ya devolvió la autorización original y `ON CONFLICT` deja
        // el enlace que ya existía.
        sqlx::query!(
            r#"
            INSERT INTO authorization_mandates (authorization_id, mandate_id)
            VALUES ($1, $2)
            ON CONFLICT (authorization_id) DO NOTHING
            "#,
            result.authorization.id,
            mandate_id,
        )
        .execute(&mut *tx)
        .await?;

        tx.commit().await?;

        let consumed_after = if result.replayed {
            consumed
        } else {
            consumed + command.amount_micros
        };
        Ok((result, MandateWithUsage { mandate, consumed_micros: consumed_after }))
    }

    pub async fn find(&self, id: Uuid) -> Result<Option<MandateWithUsage>, MandateError> {
        let Some(mandate) = self.find_mandate(id).await? else {
            return Ok(None);
        };
        let consumed = sqlx::query_scalar!(
            "SELECT consumed_micros FROM mandate_consumption WHERE mandate_id = $1",
            id,
        )
        .fetch_optional(&self.pool)
        .await?
        .flatten()
        .unwrap_or(0);

        Ok(Some(MandateWithUsage { mandate, consumed_micros: consumed }))
    }

    /// Busca el mandato vivo de una integración sobre una cuenta.
    ///
    /// Es la comprobación que reemplaza a "¿esta cuenta es tuya?" en el modelo B.
    pub async fn find_active_for(
        &self,
        account_id: Uuid,
        grantee: &str,
    ) -> Result<Option<Mandate>, MandateError> {
        let mandates = sqlx::query_as!(
            Mandate,
            r#"
            SELECT id, account_id, grantee, granted_by, consent_reference, currency,
                   max_per_operation_micros, max_total_micros,
                   status AS "status: MandateStatus", expires_at, created_at,
                   revoked_at, revoked_by
              FROM payment_mandates
             WHERE account_id = $1 AND grantee = $2 AND status = 'ACTIVE'
               AND expires_at > now()
             ORDER BY created_at DESC
             LIMIT 1
            "#,
            account_id,
            grantee,
        )
        .fetch_optional(&self.pool)
        .await?;
        Ok(mandates)
    }

    /// Mandatos de un titular, para que los vea y los revoque en su app.
    pub async fn list_for_holder(&self, granted_by: Uuid) -> Result<Vec<Mandate>, MandateError> {
        let mandates = sqlx::query_as!(
            Mandate,
            r#"
            SELECT id, account_id, grantee, granted_by, consent_reference, currency,
                   max_per_operation_micros, max_total_micros,
                   status AS "status: MandateStatus", expires_at, created_at,
                   revoked_at, revoked_by
              FROM payment_mandates
             WHERE granted_by = $1
             ORDER BY created_at DESC
            "#,
            granted_by,
        )
        .fetch_all(&self.pool)
        .await?;
        Ok(mandates)
    }

    async fn find_mandate(&self, id: Uuid) -> Result<Option<Mandate>, MandateError> {
        let mandate = sqlx::query_as!(
            Mandate,
            r#"
            SELECT id, account_id, grantee, granted_by, consent_reference, currency,
                   max_per_operation_micros, max_total_micros,
                   status AS "status: MandateStatus", expires_at, created_at,
                   revoked_at, revoked_by
              FROM payment_mandates WHERE id = $1
            "#,
            id,
        )
        .fetch_optional(&self.pool)
        .await?;
        Ok(mandate)
    }
}

// ---------------------------------------------------------------- internos

fn validate_grant(command: &GrantCommand) -> Result<(), MandateError> {
    if command.grantee.trim().is_empty() {
        return Err(MandateError::Invalid("grantee must not be blank".into()));
    }
    if command.consent_reference.trim().is_empty() {
        // Sin evidencia del consentimiento, el mandato no prueba nada — que es
        // lo único que justifica su existencia.
        return Err(MandateError::Invalid(
            "consent_reference is required: a mandate without evidence proves nothing".into(),
        ));
    }
    if command.expires_at <= Utc::now() {
        return Err(MandateError::Invalid(
            "expires_at must be in the future".into(),
        ));
    }
    for (name, value) in [
        ("max_per_operation_micros", command.max_per_operation_micros),
        ("max_total_micros", command.max_total_micros),
    ] {
        if value.is_some_and(|v| v <= 0) {
            return Err(MandateError::Invalid(format!("{name} must be positive")));
        }
    }
    Ok(())
}

fn check_usable(mandate: &Mandate) -> Result<(), MandateError> {
    match mandate.observed() {
        ObservedMandateStatus::Active => Ok(()),
        ObservedMandateStatus::Revoked => Err(MandateError::Revoked(mandate.id)),
        ObservedMandateStatus::Expired => {
            Err(MandateError::Expired(mandate.id, mandate.expires_at))
        }
    }
}

/// Toma la fila del mandato con `FOR UPDATE`.
///
/// Serializa las autorizaciones que se apoyan en él. Sin el lock, dos pagos
/// concurrentes leerían el mismo consumo y ambos pasarían un tope que solo
/// alcanzaba para uno.
async fn lock_mandate(
    tx: &mut Transaction<'_, Postgres>,
    id: Uuid,
) -> Result<Mandate, MandateError> {
    sqlx::query_as!(
        Mandate,
        r#"
        SELECT id, account_id, grantee, granted_by, consent_reference, currency,
               max_per_operation_micros, max_total_micros,
               status AS "status: MandateStatus", expires_at, created_at,
               revoked_at, revoked_by
          FROM payment_mandates
         WHERE id = $1
           FOR UPDATE
        "#,
        id,
    )
    .fetch_optional(&mut **tx)
    .await?
    .ok_or(MandateError::NotFound(id))
}

async fn consumption_of(
    tx: &mut Transaction<'_, Postgres>,
    mandate_id: Uuid,
) -> Result<i64, MandateError> {
    let consumed = sqlx::query_scalar!(
        "SELECT consumed_micros FROM mandate_consumption WHERE mandate_id = $1",
        mandate_id,
    )
    .fetch_optional(&mut **tx)
    .await?
    .flatten()
    .unwrap_or(0);
    Ok(consumed)
}

//! Autorizaciones: reservar dinero ahora, moverlo después.
//!
//! Es el patrón de una preautorización de tarjeta, generalizado a cualquier pago
//! en dos tiempos. El comprador reserva un monto (**autorizar**) y recién después
//! se mueve el dinero de verdad (**capturar**).
//!
//! # Por qué vive en el core y no en un adaptador
//!
//! Porque el pagador y el receptor son dos cuentas del banco: capturar es una sola
//! transacción de ledger. Si la máquina de estados viviera fuera, el asiento y el
//! cambio de estado se confirmarían por separado y un proceso que muere en el medio
//! dejaría dinero movido con la autorización diciendo que sigue retenida. Aquí las
//! dos cosas viven en la misma transacción de base de datos y no hay ventana.
//!
//! # La cuenta de retención es PASIVO
//!
//! El dinero de una autorización viva salió del saldo disponible del pagador pero
//! **sigue siendo del cliente**: el banco se lo debe. Tipificarla como activo diría
//! que el dinero es del banco, que es falso hasta que se capture.
//!
//! # Vencimiento
//!
//! Toda autorización nace con `expires_at`. Sin liberación automática, una
//! autorización que nadie captura inmoviliza el dinero del pagador para siempre.
//! Una autorización pasada de fecha y todavía no barrida se lee como vencida y
//! rechaza la captura, aunque el dinero siga retenido: el estado que se reporta
//! nunca promete algo distinto de lo que la contabilidad puede cumplir.

use crate::model::*;
use crate::posting::post_in_tx;
use chrono::{DateTime, Duration, Utc};
use sha2::{Digest, Sha256};
use sqlx::{PgPool, Postgres, Transaction};
use uuid::Uuid;

/// Vigencia por defecto de una autorización.
///
/// Quince minutos: quien captura en el mismo instante —el caso normal de un pago
/// entre agentes— tiene margen de sobra para reintentos de red, y quien nunca
/// captura no deja el dinero del pagador retenido más que un cuarto de hora.
pub const DEFAULT_TTL_MINUTES: i64 = 15;

/// Prefijo del código de la cuenta de retención por moneda: `AUTH-HOLDS-USD`.
pub const HOLDS_ACCOUNT_PREFIX: &str = "AUTH-HOLDS-";

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "authorization_status", rename_all = "SCREAMING_SNAKE_CASE")]
pub enum AuthorizationStatus {
    Authorized,
    Captured,
    PartiallyRefunded,
    Refunded,
    Released,
}

/// Estado tal como se le reporta a quien consulta.
///
/// Se deriva del estado almacenado y del reloj, y por eso no es el mismo tipo:
/// `Expired` no es un estado que se guarde, es lo que significa una autorización
/// viva cuya fecha ya pasó. Guardarlo obligaría a escribir en la base cada vez que
/// el tiempo avanza, que es justamente lo que no se puede hacer.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ObservedStatus {
    Authorized,
    Captured,
    PartiallyRefunded,
    Refunded,
    Released,
    Expired,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Authorization {
    pub id: Uuid,
    pub idempotency_key: String,
    pub payer_account_id: Uuid,
    pub payee_account_id: Uuid,
    pub amount_micros: i64,
    pub currency: String,
    pub status: AuthorizationStatus,
    pub refunded_micros: i64,
    pub hold_transaction_id: Uuid,
    pub settle_transaction_id: Option<Uuid>,
    pub expires_at: DateTime<Utc>,
    pub created_at: DateTime<Utc>,
    pub settled_at: Option<DateTime<Utc>>,
    pub released_at: Option<DateTime<Utc>>,
}

impl Authorization {
    /// Estado observable en un instante dado.
    pub fn observed_at(&self, now: DateTime<Utc>) -> ObservedStatus {
        match self.status {
            AuthorizationStatus::Authorized if self.expires_at <= now => ObservedStatus::Expired,
            AuthorizationStatus::Authorized => ObservedStatus::Authorized,
            AuthorizationStatus::Captured => ObservedStatus::Captured,
            AuthorizationStatus::PartiallyRefunded => ObservedStatus::PartiallyRefunded,
            AuthorizationStatus::Refunded => ObservedStatus::Refunded,
            AuthorizationStatus::Released => ObservedStatus::Released,
        }
    }

    pub fn observed(&self) -> ObservedStatus {
        self.observed_at(Utc::now())
    }

    /// Monto que todavía no se ha devuelto.
    pub fn refundable_micros(&self) -> i64 {
        self.amount_micros - self.refunded_micros
    }
}

/// Resultado de autorizar: la autorización y si venía de una clave ya usada.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthorizeResult {
    pub authorization: Authorization,
    /// `true` si la clave de idempotencia ya existía y se devolvió la original.
    pub replayed: bool,
}

#[derive(Debug, thiserror::Error)]
pub enum AuthorizationError {
    #[error("invalid request: {0}")]
    Invalid(String),

    /// La clave ya se usó con un payload distinto: error del llamador.
    #[error("idempotency key '{0}' was already used with a different payload")]
    IdempotencyConflict(String),

    #[error("authorization {0} not found")]
    NotFound(Uuid),

    #[error("account {0} not found")]
    AccountNotFound(Uuid),

    /// La cuenta existe pero no admite movimientos (congelada o cerrada).
    #[error("account {0} is not operative: {1}")]
    AccountNotOperative(Uuid, String),

    /// La retención venció; el dinero se libera, no se captura.
    #[error("authorization {0} expired at {1}")]
    Expired(Uuid, DateTime<Utc>),

    /// La operación no aplica al estado actual (capturar dos veces, reembolsar sin capturar).
    #[error("authorization {0} is {1}: {2}")]
    InvalidState(Uuid, String, String),

    #[error("insufficient funds: {0}")]
    InsufficientFunds(String),

    #[error("no holds account configured for {0}")]
    MissingHoldsAccount(String),

    #[error(transparent)]
    Posting(#[from] PostingError),

    #[error(transparent)]
    Database(#[from] sqlx::Error),
}

/// Orden de autorización.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthorizeCommand {
    pub idempotency_key: String,
    pub payer_account_id: Uuid,
    pub payee_account_id: Uuid,
    pub amount_micros: i64,
    pub currency: String,
    /// Vigencia. `None` usa [`DEFAULT_TTL_MINUTES`].
    pub ttl_minutes: Option<i64>,
}

pub struct AuthorizationService {
    pool: PgPool,
}

impl AuthorizationService {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Reserva el monto en la cuenta pagadora a favor de la receptora.
    ///
    /// Debe pagador / haber retención, más la fila de autorización, en una sola
    /// transacción.
    pub async fn authorize(
        &self,
        command: &AuthorizeCommand,
    ) -> Result<AuthorizeResult, AuthorizationError> {
        validate(command)?;
        let hash = command_hash(command);

        let mut tx = self.pool.begin().await?;

        // Si la clave ya existe, se devuelve la original sin volver a retener.
        if let Some((existing, existing_hash)) =
            find_by_key(&mut tx, &command.idempotency_key).await?
        {
            if existing_hash != hash {
                return Err(AuthorizationError::IdempotencyConflict(
                    command.idempotency_key.clone(),
                ));
            }
            return Ok(AuthorizeResult { authorization: existing, replayed: true });
        }

        let payer = load_operative_account(&mut tx, command.payer_account_id).await?;
        let payee = load_operative_account(&mut tx, command.payee_account_id).await?;

        if payer.currency != command.currency || payee.currency != command.currency {
            return Err(AuthorizationError::Invalid(format!(
                "currency mismatch: payer {}, payee {}, requested {}",
                payer.currency, payee.currency, command.currency
            )));
        }

        let holds = find_holds_account(&mut tx, &command.currency).await?;

        let posting = PostingRequest::new(
            format!("auth-hold-{}", command.idempotency_key),
            "authorization_hold",
            vec![
                EntryCommand::debit(payer.id, command.amount_micros, &command.currency),
                EntryCommand::credit(holds, command.amount_micros, &command.currency),
            ],
        );
        let hold = post_in_tx(&mut tx, &posting).await?;

        let ttl = command.ttl_minutes.unwrap_or(DEFAULT_TTL_MINUTES);
        let expires_at = Utc::now() + Duration::minutes(ttl);

        let row = sqlx::query!(
            r#"
            INSERT INTO authorizations (
                idempotency_key, request_hash, payer_account_id, payee_account_id,
                amount_micros, currency, hold_transaction_id, expires_at
            )
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
            RETURNING id, created_at
            "#,
            command.idempotency_key,
            hash,
            payer.id,
            payee.id,
            command.amount_micros,
            command.currency,
            hold.transaction.id,
            expires_at,
        )
        .fetch_one(&mut *tx)
        .await
        .map_err(map_db_error)?;

        // El COMMIT es donde corren los triggers diferidos: si el pagador no tenía
        // saldo, la retención Y la autorización se pierden juntas.
        tx.commit().await.map_err(map_db_error)?;

        Ok(AuthorizeResult {
            authorization: Authorization {
                id: row.id,
                idempotency_key: command.idempotency_key.clone(),
                payer_account_id: payer.id,
                payee_account_id: payee.id,
                amount_micros: command.amount_micros,
                currency: command.currency.clone(),
                status: AuthorizationStatus::Authorized,
                refunded_micros: 0,
                hold_transaction_id: hold.transaction.id,
                settle_transaction_id: None,
                expires_at,
                created_at: row.created_at,
                settled_at: None,
                released_at: None,
            },
            replayed: false,
        })
    }

    /// Efectiviza una autorización: mueve el dinero de la retención al receptor.
    ///
    /// Es idempotente por naturaleza. Capturar dos veces la misma autorización
    /// devuelve la misma captura con el mismo `settled_at`, no un error — un
    /// timeout de red no puede distinguirse de un fallo real desde el cliente, y
    /// responder "ya capturada" a un reintento legítimo obliga a adivinar si el
    /// dinero se movió.
    pub async fn capture(&self, id: Uuid) -> Result<Authorization, AuthorizationError> {
        let mut tx = self.pool.begin().await?;

        let auth = lock_authorization(&mut tx, id).await?;

        match auth.status {
            AuthorizationStatus::Captured
            | AuthorizationStatus::PartiallyRefunded
            | AuthorizationStatus::Refunded => {
                // Ya se movió el dinero: se devuelve tal cual está.
                return Ok(auth);
            }
            AuthorizationStatus::Released => {
                return Err(AuthorizationError::InvalidState(
                    id,
                    "released".into(),
                    "the hold was already returned to the payer".into(),
                ));
            }
            AuthorizationStatus::Authorized => {}
        }

        let now = Utc::now();
        if auth.expires_at <= now {
            return Err(AuthorizationError::Expired(id, auth.expires_at));
        }

        let holds = find_holds_account(&mut tx, &auth.currency).await?;

        let posting = PostingRequest::new(
            format!("auth-capture-{id}"),
            "authorization_capture",
            vec![
                EntryCommand::debit(holds, auth.amount_micros, &auth.currency),
                EntryCommand::credit(auth.payee_account_id, auth.amount_micros, &auth.currency),
            ],
        );
        let settle = post_in_tx(&mut tx, &posting).await?;

        let row = sqlx::query!(
            r#"
            UPDATE authorizations
               SET status = 'CAPTURED', settle_transaction_id = $2, settled_at = $3
             WHERE id = $1
            RETURNING settled_at
            "#,
            id,
            settle.transaction.id,
            now,
        )
        .fetch_one(&mut *tx)
        .await
        .map_err(map_db_error)?;

        tx.commit().await.map_err(map_db_error)?;

        Ok(Authorization {
            status: AuthorizationStatus::Captured,
            settle_transaction_id: Some(settle.transaction.id),
            settled_at: row.settled_at,
            ..auth
        })
    }

    /// Devuelve dinero ya capturado al pagador.
    ///
    /// `amount_micros` en `None` reembolsa lo que quede sin devolver, que es el
    /// caso total. El parcial existe en el modelo desde el principio.
    pub async fn refund(
        &self,
        id: Uuid,
        amount_micros: Option<i64>,
    ) -> Result<Authorization, AuthorizationError> {
        let mut tx = self.pool.begin().await?;

        let auth = lock_authorization(&mut tx, id).await?;

        match auth.status {
            AuthorizationStatus::Authorized => {
                return Err(AuthorizationError::InvalidState(
                    id,
                    "authorized".into(),
                    "nothing to refund: the money was never captured".into(),
                ));
            }
            AuthorizationStatus::Released => {
                return Err(AuthorizationError::InvalidState(
                    id,
                    "released".into(),
                    "the hold expired and was already returned to the payer".into(),
                ));
            }
            AuthorizationStatus::Refunded => return Ok(auth),
            AuthorizationStatus::Captured | AuthorizationStatus::PartiallyRefunded => {}
        }

        let remaining = auth.refundable_micros();
        let amount = amount_micros.unwrap_or(remaining);

        if amount <= 0 {
            return Err(AuthorizationError::Invalid(
                "refund amount must be positive".into(),
            ));
        }
        if amount > remaining {
            return Err(AuthorizationError::Invalid(format!(
                "refund of {amount} exceeds the {remaining} still refundable"
            )));
        }

        // La clave incluye el acumulado ya devuelto: dos reembolsos parciales del
        // mismo monto son operaciones distintas y no deben colapsar en una, pero
        // reintentar EL MISMO reembolso sí devuelve el original.
        let refund_key = format!("auth-refund-{id}-{}", auth.refunded_micros);

        let posting = PostingRequest::new(
            refund_key.clone(),
            "authorization_refund",
            vec![
                EntryCommand::debit(auth.payee_account_id, amount, &auth.currency),
                EntryCommand::credit(auth.payer_account_id, amount, &auth.currency),
            ],
        );
        let refund = post_in_tx(&mut tx, &posting).await?;

        sqlx::query!(
            r#"
            INSERT INTO authorization_refunds (authorization_id, idempotency_key, amount_micros, transaction_id)
            VALUES ($1, $2, $3, $4)
            ON CONFLICT (idempotency_key) DO NOTHING
            "#,
            id,
            refund_key,
            amount,
            refund.transaction.id,
        )
        .execute(&mut *tx)
        .await
        .map_err(map_db_error)?;

        let refunded_total = auth.refunded_micros + amount;
        let new_status = if refunded_total >= auth.amount_micros {
            AuthorizationStatus::Refunded
        } else {
            AuthorizationStatus::PartiallyRefunded
        };

        sqlx::query!(
            r#"
            UPDATE authorizations SET status = $2, refunded_micros = $3 WHERE id = $1
            "#,
            id,
            new_status as AuthorizationStatus,
            refunded_total,
        )
        .execute(&mut *tx)
        .await
        .map_err(map_db_error)?;

        tx.commit().await.map_err(map_db_error)?;

        Ok(Authorization { status: new_status, refunded_micros: refunded_total, ..auth })
    }

    /// Libera una retención vencida: el dinero vuelve al pagador.
    pub async fn release(&self, id: Uuid) -> Result<Authorization, AuthorizationError> {
        let mut tx = self.pool.begin().await?;
        let auth = lock_authorization(&mut tx, id).await?;
        let released = release_locked(&mut tx, &auth).await?;
        tx.commit().await.map_err(map_db_error)?;
        Ok(released)
    }

    /// Libera todas las autorizaciones vencidas y devuelve cuántas.
    ///
    /// Cada una en su propia transacción: una que falle —porque la cuenta del
    /// pagador se cerró, por ejemplo— no puede impedir que el resto del dinero
    /// vuelva a sus dueños.
    pub async fn release_expired(&self, limit: i64) -> Result<Vec<Uuid>, AuthorizationError> {
        let expired = sqlx::query!(
            r#"
            SELECT id FROM authorizations
             WHERE status = 'AUTHORIZED' AND expires_at <= now()
             ORDER BY expires_at
             LIMIT $1
            "#,
            limit,
        )
        .fetch_all(&self.pool)
        .await?;

        let mut released = Vec::new();
        for row in expired {
            match self.release(row.id).await {
                Ok(_) => released.push(row.id),
                Err(err) => {
                    tracing::warn!(
                        authorization_id = %row.id,
                        error = %err,
                        "no se pudo liberar una autorización vencida",
                    );
                }
            }
        }
        Ok(released)
    }

    pub async fn find(&self, id: Uuid) -> Result<Option<Authorization>, AuthorizationError> {
        let mut conn = self.pool.acquire().await?;
        find_by_id(&mut conn, id).await
    }
}

// ---------------------------------------------------------------- internos

async fn release_locked(
    tx: &mut Transaction<'_, Postgres>,
    auth: &Authorization,
) -> Result<Authorization, AuthorizationError> {
    match auth.status {
        AuthorizationStatus::Released => return Ok(auth.clone()),
        AuthorizationStatus::Authorized => {}
        _ => {
            return Err(AuthorizationError::InvalidState(
                auth.id,
                format!("{:?}", auth.status).to_lowercase(),
                "only a live hold can be released".into(),
            ));
        }
    }

    let holds = find_holds_account(tx, &auth.currency).await?;

    let posting = PostingRequest::new(
        format!("auth-release-{}", auth.id),
        "authorization_release",
        vec![
            EntryCommand::debit(holds, auth.amount_micros, &auth.currency),
            EntryCommand::credit(auth.payer_account_id, auth.amount_micros, &auth.currency),
        ],
    );
    post_in_tx(tx, &posting).await?;

    let now = Utc::now();
    sqlx::query!(
        "UPDATE authorizations SET status = 'RELEASED', released_at = $2 WHERE id = $1",
        auth.id,
        now,
    )
    .execute(&mut **tx)
    .await
    .map_err(map_db_error)?;

    Ok(Authorization {
        status: AuthorizationStatus::Released,
        released_at: Some(now),
        ..auth.clone()
    })
}

fn validate(command: &AuthorizeCommand) -> Result<(), AuthorizationError> {
    if command.idempotency_key.trim().is_empty() {
        return Err(AuthorizationError::Invalid(
            "idempotency_key must not be blank".into(),
        ));
    }
    if command.amount_micros <= 0 {
        return Err(AuthorizationError::Invalid(format!(
            "amount must be positive, got {}",
            command.amount_micros
        )));
    }
    if command.currency.len() != 3 {
        return Err(AuthorizationError::Invalid(format!(
            "currency must be ISO-4217, got '{}'",
            command.currency
        )));
    }
    if command.payer_account_id == command.payee_account_id {
        return Err(AuthorizationError::Invalid(
            "payer and payee must be different accounts".into(),
        ));
    }
    if command.ttl_minutes.is_some_and(|ttl| ttl <= 0) {
        return Err(AuthorizationError::Invalid(
            "ttl_minutes must be positive".into(),
        ));
    }
    Ok(())
}

fn command_hash(command: &AuthorizeCommand) -> String {
    let mut hasher = Sha256::new();
    hasher.update(command.payer_account_id.as_bytes());
    hasher.update(command.payee_account_id.as_bytes());
    hasher.update(command.amount_micros.to_be_bytes());
    hasher.update(command.currency.as_bytes());
    format!("{:x}", hasher.finalize())
}

struct OperativeAccount {
    id: Uuid,
    currency: String,
}

async fn load_operative_account(
    tx: &mut Transaction<'_, Postgres>,
    id: Uuid,
) -> Result<OperativeAccount, AuthorizationError> {
    let row = sqlx::query!(
        r#"SELECT id, currency, status AS "status: AccountStatus" FROM accounts WHERE id = $1"#,
        id,
    )
    .fetch_optional(&mut **tx)
    .await?
    .ok_or(AuthorizationError::AccountNotFound(id))?;

    match row.status {
        AccountStatus::Active => Ok(OperativeAccount { id: row.id, currency: row.currency }),
        AccountStatus::Frozen => Err(AuthorizationError::AccountNotOperative(
            id,
            "frozen".into(),
        )),
        AccountStatus::Closed => Err(AuthorizationError::AccountNotOperative(
            id,
            "closed".into(),
        )),
    }
}

async fn find_holds_account(
    tx: &mut Transaction<'_, Postgres>,
    currency: &str,
) -> Result<Uuid, AuthorizationError> {
    let code = format!("{HOLDS_ACCOUNT_PREFIX}{currency}");
    sqlx::query!("SELECT id FROM accounts WHERE code = $1", code)
        .fetch_optional(&mut **tx)
        .await?
        .map(|row| row.id)
        .ok_or(AuthorizationError::MissingHoldsAccount(code))
}

/// Toma la fila con `FOR UPDATE`.
///
/// Serializa capturar, reembolsar y liberar sobre la misma autorización. Sin este
/// lock, dos capturas concurrentes leerían ambas `AUTHORIZED` y el trigger de
/// transición rechazaría a la segunda **después** de haber asentado su
/// movimiento — el dinero se movería dos veces y una de las dos transacciones se
/// caería al final. El lock convierte esa carrera en una espera.
async fn lock_authorization(
    tx: &mut Transaction<'_, Postgres>,
    id: Uuid,
) -> Result<Authorization, AuthorizationError> {
    sqlx::query_as!(
        Authorization,
        r#"
        SELECT id, idempotency_key, payer_account_id, payee_account_id, amount_micros,
               currency, status AS "status: AuthorizationStatus", refunded_micros,
               hold_transaction_id, settle_transaction_id, expires_at, created_at,
               settled_at, released_at
          FROM authorizations
         WHERE id = $1
           FOR UPDATE
        "#,
        id,
    )
    .fetch_optional(&mut **tx)
    .await?
    .ok_or(AuthorizationError::NotFound(id))
}

async fn find_by_key(
    tx: &mut Transaction<'_, Postgres>,
    key: &str,
) -> Result<Option<(Authorization, String)>, AuthorizationError> {
    let row = sqlx::query!(
        r#"
        SELECT id, idempotency_key, request_hash, payer_account_id, payee_account_id,
               amount_micros, currency, status AS "status: AuthorizationStatus",
               refunded_micros, hold_transaction_id, settle_transaction_id, expires_at,
               created_at, settled_at, released_at
          FROM authorizations
         WHERE idempotency_key = $1
        "#,
        key,
    )
    .fetch_optional(&mut **tx)
    .await?;

    Ok(row.map(|r| {
        (
            Authorization {
                id: r.id,
                idempotency_key: r.idempotency_key,
                payer_account_id: r.payer_account_id,
                payee_account_id: r.payee_account_id,
                amount_micros: r.amount_micros,
                currency: r.currency,
                status: r.status,
                refunded_micros: r.refunded_micros,
                hold_transaction_id: r.hold_transaction_id,
                settle_transaction_id: r.settle_transaction_id,
                expires_at: r.expires_at,
                created_at: r.created_at,
                settled_at: r.settled_at,
                released_at: r.released_at,
            },
            r.request_hash,
        )
    }))
}

async fn find_by_id(
    conn: &mut sqlx::PgConnection,
    id: Uuid,
) -> Result<Option<Authorization>, AuthorizationError> {
    let auth = sqlx::query_as!(
        Authorization,
        r#"
        SELECT id, idempotency_key, payer_account_id, payee_account_id, amount_micros,
               currency, status AS "status: AuthorizationStatus", refunded_micros,
               hold_transaction_id, settle_transaction_id, expires_at, created_at,
               settled_at, released_at
          FROM authorizations
         WHERE id = $1
        "#,
        id,
    )
    .fetch_optional(&mut *conn)
    .await?;
    Ok(auth)
}

/// Traduce los SQLSTATE propios a errores de negocio.
///
/// Los triggers diferidos del ledger corren en el COMMIT, así que "fondos
/// insuficientes" llega como error de `commit()` y no de la consulta que retuvo.
fn map_db_error(err: sqlx::Error) -> AuthorizationError {
    if let Some(db_err) = err.as_database_error() {
        let message = db_err.message().to_owned();
        match db_err.code().as_deref() {
            Some("AB001") => return AuthorizationError::InsufficientFunds(message),
            Some("AB004") => {
                return AuthorizationError::Invalid(format!("illegal state transition: {message}"))
            }
            _ => {}
        }
    }
    AuthorizationError::Database(err)
}

//! Tipos del dominio contable.
//!
//! Los montos son SIEMPRE `i64` en unidades menores (centavos) y positivos;
//! el signo lo aporta [`Direction`]. Nunca coma flotante.

use chrono::{DateTime, Utc};
use uuid::Uuid;

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "account_type", rename_all = "UPPERCASE")]
pub enum AccountType {
    Asset,
    Liability,
    Equity,
    Income,
    Expense,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "account_owner", rename_all = "UPPERCASE")]
pub enum AccountOwner {
    Customer,
    Internal,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "account_status", rename_all = "UPPERCASE")]
pub enum AccountStatus {
    Active,
    Frozen,
    Closed,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "entry_direction", rename_all = "UPPERCASE")]
pub enum Direction {
    Debit,
    Credit,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Account {
    pub id: Uuid,
    pub code: String,
    pub name: String,
    pub account_type: AccountType,
    pub owner: AccountOwner,
    pub owner_id: Option<Uuid>,
    pub currency: String,
    pub status: AccountStatus,
    pub created_at: DateTime<Utc>,
}

/// Orden de asiento dentro de una solicitud de posting.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct EntryCommand {
    pub account_id: Uuid,
    pub direction: Direction,
    pub amount_minor: i64,
    pub currency: String,
}

impl EntryCommand {
    pub fn debit(account_id: Uuid, amount_minor: i64, currency: &str) -> Self {
        Self { account_id, direction: Direction::Debit, amount_minor, currency: currency.to_owned() }
    }

    pub fn credit(account_id: Uuid, amount_minor: i64, currency: &str) -> Self {
        Self { account_id, direction: Direction::Credit, amount_minor, currency: currency.to_owned() }
    }
}

/// Solicitud de transacción contable.
///
/// `idempotency_key` la define quien origina la operación (transferencia, webhook
/// de tarjeta, mensaje del riel) y garantiza que el efecto ocurra exactamente una vez.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PostingRequest {
    pub idempotency_key: String,
    pub kind: String,
    pub entries: Vec<EntryCommand>,
    pub description: Option<String>,
}

impl PostingRequest {
    pub fn new(idempotency_key: impl Into<String>, kind: impl Into<String>, entries: Vec<EntryCommand>) -> Self {
        Self {
            idempotency_key: idempotency_key.into(),
            kind: kind.into(),
            entries,
            description: None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LedgerTransaction {
    pub id: Uuid,
    pub idempotency_key: String,
    pub kind: String,
    pub description: Option<String>,
    pub posted_at: DateTime<Utc>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PostingResult {
    pub transaction: LedgerTransaction,
    /// `true` si la clave ya existía y se devolvió la transacción original.
    pub replayed: bool,
}

#[derive(Debug, thiserror::Error)]
pub enum PostingError {
    /// La solicitud viola una regla del ledger (desbalance, monto inválido, moneda).
    #[error("invalid posting: {0}")]
    Invalid(String),

    /// La clave ya fue usada con un payload distinto: error de programación del llamador.
    #[error("idempotency key '{0}' was already used with a different payload")]
    IdempotencyConflict(String),

    #[error(transparent)]
    Database(#[from] sqlx::Error),
}

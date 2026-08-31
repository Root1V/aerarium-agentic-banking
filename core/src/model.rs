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

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "product_kind", rename_all = "SCREAMING_SNAKE_CASE")]
pub enum ProductKind {
    DepositAccount,
}

/// Producto como configuración: abrir una cuenta hereda de aquí moneda y reglas.
/// Los topes reflejan las cuentas simplificadas de la región (nivel 1 en México,
/// dinero electrónico en Perú), donde el regulador limita saldo y monto por operación.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Product {
    pub id: Uuid,
    pub code: String,
    pub name: String,
    pub kind: ProductKind,
    pub currency: String,
    pub allows_overdraft: bool,
    pub max_balance_minor: Option<i64>,
    pub max_transaction_minor: Option<i64>,
    pub active: bool,
}

/// Definición de un producto nuevo del catálogo.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NewProduct {
    pub code: String,
    pub name: String,
    pub kind: ProductKind,
    pub currency: String,
    pub allows_overdraft: bool,
    pub max_balance_minor: Option<i64>,
    pub max_transaction_minor: Option<i64>,
}

impl NewProduct {
    /// Cuenta de depósito simple: sin sobregiro y sin topes regulatorios.
    pub fn deposit_account(code: impl Into<String>, name: impl Into<String>, currency: &str) -> Self {
        Self {
            code: code.into(),
            name: name.into(),
            kind: ProductKind::DepositAccount,
            currency: currency.to_owned(),
            allows_overdraft: false,
            max_balance_minor: None,
            max_transaction_minor: None,
        }
    }

    /// Aplica topes regulatorios de cuenta simplificada.
    pub fn with_caps(mut self, max_balance_minor: Option<i64>, max_transaction_minor: Option<i64>) -> Self {
        self.max_balance_minor = max_balance_minor;
        self.max_transaction_minor = max_transaction_minor;
        self
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Account {
    pub id: Uuid,
    pub code: String,
    pub name: String,
    pub account_type: AccountType,
    pub owner: AccountOwner,
    pub owner_id: Option<Uuid>,
    pub product_id: Option<Uuid>,
    pub currency: String,
    pub status: AccountStatus,
    pub allows_overdraft: bool,
    pub created_at: DateTime<Utc>,
}

/// Saldo materializado y su contador de asientos, para auditarlo contra la proyección.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Balance {
    pub balance_minor: i64,
    pub entry_count: i64,
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

    /// La cuenta terminaría en negativo y el producto no permite sobregiro (SQLSTATE AB001).
    #[error("insufficient funds: {0}")]
    InsufficientFunds(String),

    /// El saldo superaría el tope regulatorio del producto (SQLSTATE AB002).
    #[error("balance cap exceeded: {0}")]
    BalanceCapExceeded(String),

    /// El monto supera el tope por operación del producto (SQLSTATE AB003).
    #[error("transaction cap exceeded: {0}")]
    TransactionCapExceeded(String),

    /// Ya existe un registro con esa clave única (SQLSTATE 23505). Es un conflicto
    /// del llamador, no un fallo de infraestructura: reintentar no lo resuelve.
    #[error("already exists: {0}")]
    Conflict(String),

    #[error(transparent)]
    Database(#[from] sqlx::Error),
}

impl PostingError {
    /// Traduce los SQLSTATE propios del esquema a errores de negocio tipados,
    /// para que el llamador no tenga que interpretar mensajes de la base.
    pub(crate) fn from_db(err: sqlx::Error) -> Self {
        let Some(db_err) = err.as_database_error() else {
            return PostingError::Database(err);
        };
        let message = db_err.message().to_owned();
        match db_err.code().as_deref() {
            Some("AB001") => PostingError::InsufficientFunds(message),
            Some("AB002") => PostingError::BalanceCapExceeded(message),
            Some("AB003") => PostingError::TransactionCapExceeded(message),
            Some("23505") => PostingError::Conflict(message),
            _ => PostingError::Database(err),
        }
    }
}

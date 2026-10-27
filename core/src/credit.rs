//! Líneas de crédito: el contrato de una tarjeta de crédito sobre el ledger.
//!
//! La línea es una cuenta del cliente de tipo ACTIVO —lo que le debe al banco— más
//! las condiciones del contrato: límite, tasa, día de corte. Usarla y pagarla son
//! asientos normales del ledger; el límite, el bloqueo y el "no pagar de más" los
//! hace cumplir el mismo trigger que hace cumplir el sobregiro (V010), así que no
//! hay camino que se los salte.
//!
//! Este módulo no decide a quién se le da crédito: eso es del motor de riesgo. Aquí
//! solo se abre la línea que esa decisión otorgó, con su referencia.

use crate::model::*;
use chrono::{DateTime, Utc};
use sqlx::PgPool;
use uuid::Uuid;

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "credit_line_status", rename_all = "SCREAMING_SNAKE_CASE")]
pub enum CreditLineStatus {
    Active,
    /// Por mora: se puede pagar, no usar.
    Blocked,
    Closed,
}

/// Una línea de crédito con su estado de uso.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CreditLine {
    pub id: Uuid,
    pub account_id: Uuid,
    pub customer_id: Uuid,
    pub currency: String,
    pub limit_micros: i64,
    /// Lo que se debe hoy: el saldo de la cuenta de la línea.
    pub outstanding_micros: i64,
    pub annual_rate_bps: i32,
    pub cycle_day: i16,
    pub payment_days: i16,
    pub status: CreditLineStatus,
    pub decision_ref: String,
    pub opened_at: DateTime<Utc>,
}

impl CreditLine {
    /// Lo que todavía se puede usar.
    pub fn available_micros(&self) -> i64 {
        (self.limit_micros - self.outstanding_micros).max(0)
    }
}

/// Las condiciones con que se abre una línea.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NewCreditLine {
    pub customer_id: Uuid,
    /// Nombre de la cuenta: el del titular, como en sus otras cuentas.
    pub name: String,
    pub currency: String,
    pub limit_micros: i64,
    pub annual_rate_bps: i32,
    pub cycle_day: i16,
    pub payment_days: i16,
    pub decision_ref: String,
}

pub struct CreditRepository {
    pool: PgPool,
}

impl CreditRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Abre la línea: la cuenta y el contrato en una sola transacción. Si el
    /// cliente ya tiene una línea viva, es un conflicto.
    pub async fn open(&self, new: &NewCreditLine) -> Result<CreditLine, PostingError> {
        validate(new)?;
        let mut tx = self.pool.begin().await.map_err(PostingError::from_db)?;

        // El producto de tarjeta existe uno por moneda; se crea la primera vez. Las
        // condiciones van en la línea, que es el contrato, no en el producto.
        let product_code = format!("CREDIT-CARD-{}", new.currency);
        sqlx::query!(
            r#"
            INSERT INTO products (code, name, kind, currency, allows_overdraft)
            VALUES ($1, $2, 'CREDIT_CARD', $3, FALSE)
            ON CONFLICT (code) DO NOTHING
            "#,
            product_code,
            format!("Tarjeta de crédito {}", new.currency),
            new.currency,
        )
        .execute(&mut *tx)
        .await
        .map_err(PostingError::from_db)?;
        let product_id = sqlx::query_scalar!("SELECT id FROM products WHERE code = $1", product_code)
            .fetch_one(&mut *tx)
            .await
            .map_err(PostingError::from_db)?;

        let account_id = sqlx::query_scalar!(
            r#"
            INSERT INTO accounts (code, name, type, owner, owner_id, product_id, currency, allows_overdraft)
            VALUES ($1, $2, 'ASSET', 'CUSTOMER', $3, $4, $5, FALSE)
            RETURNING id
            "#,
            format!("CC-{}", new.customer_id.simple()),
            new.name,
            new.customer_id,
            product_id,
            new.currency,
        )
        .fetch_one(&mut *tx)
        .await
        .map_err(PostingError::from_db)?;

        let line_id = sqlx::query_scalar!(
            r#"
            INSERT INTO credit_lines (account_id, customer_id, limit_micros, annual_rate_bps,
                                      cycle_day, payment_days, decision_ref)
            VALUES ($1, $2, $3, $4, $5, $6, $7)
            RETURNING id
            "#,
            account_id,
            new.customer_id,
            new.limit_micros,
            new.annual_rate_bps,
            new.cycle_day,
            new.payment_days,
            new.decision_ref,
        )
        .fetch_one(&mut *tx)
        .await
        .map_err(PostingError::from_db)?;

        tx.commit().await.map_err(PostingError::from_db)?;
        self.find(line_id)
            .await
            .map_err(PostingError::from_db)?
            .ok_or_else(|| PostingError::Database(sqlx::Error::RowNotFound))
    }

    pub async fn find(&self, id: Uuid) -> Result<Option<CreditLine>, sqlx::Error> {
        let row = sqlx::query_as!(
            LineRow,
            r#"
            SELECT l.id, l.account_id, l.customer_id, a.currency, l.limit_micros,
                   b.balance_micros as outstanding_micros, l.annual_rate_bps, l.cycle_day,
                   l.payment_days, l.status as "status: CreditLineStatus", l.decision_ref, l.opened_at
            FROM credit_lines l
            JOIN accounts a ON a.id = l.account_id
            JOIN account_balances b ON b.account_id = l.account_id
            WHERE l.id = $1
            "#,
            id,
        )
        .fetch_optional(&self.pool)
        .await?;
        Ok(row.map(Into::into))
    }

    /// La línea viva (activa o bloqueada) del cliente, si tiene.
    pub async fn find_open_for_customer(&self, customer_id: Uuid) -> Result<Option<CreditLine>, sqlx::Error> {
        let row = sqlx::query_as!(
            LineRow,
            r#"
            SELECT l.id, l.account_id, l.customer_id, a.currency, l.limit_micros,
                   b.balance_micros as outstanding_micros, l.annual_rate_bps, l.cycle_day,
                   l.payment_days, l.status as "status: CreditLineStatus", l.decision_ref, l.opened_at
            FROM credit_lines l
            JOIN accounts a ON a.id = l.account_id
            JOIN account_balances b ON b.account_id = l.account_id
            WHERE l.customer_id = $1 AND l.status <> 'CLOSED'
            "#,
            customer_id,
        )
        .fetch_optional(&self.pool)
        .await?;
        Ok(row.map(Into::into))
    }
}

struct LineRow {
    id: Uuid,
    account_id: Uuid,
    customer_id: Uuid,
    currency: String,
    limit_micros: i64,
    outstanding_micros: i64,
    annual_rate_bps: i32,
    cycle_day: i16,
    payment_days: i16,
    status: CreditLineStatus,
    decision_ref: String,
    opened_at: DateTime<Utc>,
}

impl From<LineRow> for CreditLine {
    fn from(r: LineRow) -> Self {
        CreditLine {
            id: r.id,
            account_id: r.account_id,
            customer_id: r.customer_id,
            currency: r.currency,
            limit_micros: r.limit_micros,
            outstanding_micros: r.outstanding_micros,
            annual_rate_bps: r.annual_rate_bps,
            cycle_day: r.cycle_day,
            payment_days: r.payment_days,
            status: r.status,
            decision_ref: r.decision_ref,
            opened_at: r.opened_at,
        }
    }
}

fn validate(new: &NewCreditLine) -> Result<(), PostingError> {
    let fail = |m: &str| Err(PostingError::Invalid(m.to_owned()));
    if new.limit_micros <= 0 {
        return fail("limit must be positive");
    }
    if new.annual_rate_bps < 0 {
        return fail("annual rate must not be negative");
    }
    if !(1..=28).contains(&new.cycle_day) || !(1..=28).contains(&new.payment_days) {
        return fail("cycle_day and payment_days must be between 1 and 28");
    }
    if new.currency.len() != 3 {
        return fail("currency must be ISO-4217");
    }
    if new.decision_ref.trim().is_empty() {
        return fail("a credit line needs the reference of the decision that granted it");
    }
    if new.name.trim().is_empty() {
        return fail("name must not be blank");
    }
    Ok(())
}

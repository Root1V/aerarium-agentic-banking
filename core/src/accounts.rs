//! Cuentas: alta y consulta. Los saldos se leen de la vista `account_balances`,
//! que los deriva de los asientos — nunca de un campo mutable.

use crate::model::*;
use sqlx::{PgExecutor, PgPool};
use uuid::Uuid;

pub struct AccountRepository {
    pool: PgPool,
}

impl AccountRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    pub async fn create(
        &self,
        code: &str,
        name: &str,
        account_type: AccountType,
        owner: AccountOwner,
        owner_id: Option<Uuid>,
        currency: &str,
    ) -> Result<Account, PostingError> {
        if currency.len() != 3 {
            return Err(PostingError::Invalid(format!(
                "currency must be ISO-4217, got '{currency}'"
            )));
        }
        Ok(create_account(&self.pool, code, name, account_type, owner, owner_id, currency).await?)
    }

    pub async fn find_by_code(&self, code: &str) -> Result<Option<Account>, sqlx::Error> {
        sqlx::query!(
            r#"
            SELECT id, code, name,
                   type   as "account_type: AccountType",
                   owner  as "owner: AccountOwner",
                   owner_id, currency,
                   status as "status: AccountStatus",
                   created_at
            FROM accounts WHERE code = $1
            "#,
            code,
        )
        .fetch_optional(&self.pool)
        .await
        .map(|opt| {
            opt.map(|r| Account {
                id: r.id,
                code: r.code,
                name: r.name,
                account_type: r.account_type,
                owner: r.owner,
                owner_id: r.owner_id,
                currency: r.currency,
                status: r.status,
                created_at: r.created_at,
            })
        })
    }

    /// Saldo natural de la cuenta, proyectado desde el ledger.
    pub async fn balance_minor(&self, account_id: Uuid) -> Result<i64, sqlx::Error> {
        let row = sqlx::query!(
            r#"SELECT balance_minor as "balance_minor!" FROM account_balances WHERE account_id = $1"#,
            account_id,
        )
        .fetch_optional(&self.pool)
        .await?;

        match row {
            Some(r) => Ok(r.balance_minor),
            None => Err(sqlx::Error::RowNotFound),
        }
    }
}

async fn create_account<'e, E: PgExecutor<'e>>(
    executor: E,
    code: &str,
    name: &str,
    account_type: AccountType,
    owner: AccountOwner,
    owner_id: Option<Uuid>,
    currency: &str,
) -> Result<Account, sqlx::Error> {
    let r = sqlx::query!(
        r#"
        INSERT INTO accounts (code, name, type, owner, owner_id, currency)
        VALUES ($1, $2, $3, $4, $5, $6)
        RETURNING id, code, name,
                  type   as "account_type: AccountType",
                  owner  as "owner: AccountOwner",
                  owner_id, currency,
                  status as "status: AccountStatus",
                  created_at
        "#,
        code,
        name,
        account_type as AccountType,
        owner as AccountOwner,
        owner_id,
        currency,
    )
    .fetch_one(executor)
    .await?;

    Ok(Account {
        id: r.id,
        code: r.code,
        name: r.name,
        account_type: r.account_type,
        owner: r.owner,
        owner_id: r.owner_id,
        currency: r.currency,
        status: r.status,
        created_at: r.created_at,
    })
}

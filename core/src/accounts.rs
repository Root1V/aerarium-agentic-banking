//! Cuentas: apertura desde el catálogo de productos y consulta de saldo.
//!
//! Dos caminos deliberadamente distintos:
//! - [`AccountRepository::open_customer_account`]: cuenta de cliente, siempre nacida
//!   de un producto, del que hereda moneda y reglas. Nunca admite sobregiro salvo
//!   que el producto lo diga.
//! - [`AccountRepository::create_internal`]: cuentas propias del banco (caja,
//!   settlement con proveedores, comisiones), que sí pueden quedar en negativo
//!   porque representan posiciones, no dinero de un cliente.

use crate::model::*;
use sqlx::PgPool;
use uuid::Uuid;

pub struct AccountRepository {
    pool: PgPool,
}

impl AccountRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Abre una cuenta de cliente a partir de un producto del catálogo.
    ///
    /// El saldo del cliente es un PASIVO del banco: crece al haber.
    pub async fn open_customer_account(
        &self,
        code: &str,
        name: &str,
        customer_id: Uuid,
        product: &Product,
    ) -> Result<Account, PostingError> {
        if !product.active {
            return Err(PostingError::Invalid(format!(
                "product '{}' is not active",
                product.code
            )));
        }

        self.insert(
            code,
            name,
            AccountType::Liability,
            AccountOwner::Customer,
            Some(customer_id),
            Some(product.id),
            &product.currency,
            product.allows_overdraft,
        )
        .await
    }

    /// Crea una cuenta interna del banco. Admite saldo negativo por defecto:
    /// representa una posición contable propia, no fondos de un cliente.
    pub async fn create_internal(
        &self,
        code: &str,
        name: &str,
        account_type: AccountType,
        currency: &str,
    ) -> Result<Account, PostingError> {
        if currency.len() != 3 {
            return Err(PostingError::Invalid(format!(
                "currency must be ISO-4217, got '{currency}'"
            )));
        }
        self.insert(code, name, account_type, AccountOwner::Internal, None, None, currency, true)
            .await
    }

    pub async fn find_by_code(&self, code: &str) -> Result<Option<Account>, sqlx::Error> {
        let row = sqlx::query!(
            r#"
            SELECT id, code, name,
                   type   as "account_type: AccountType",
                   owner  as "owner: AccountOwner",
                   owner_id, product_id, currency,
                   status as "status: AccountStatus",
                   allows_overdraft, created_at
            FROM accounts WHERE code = $1
            "#,
            code,
        )
        .fetch_optional(&self.pool)
        .await?;

        Ok(row.map(|r| Account {
            id: r.id,
            code: r.code,
            name: r.name,
            account_type: r.account_type,
            owner: r.owner,
            owner_id: r.owner_id,
            product_id: r.product_id,
            currency: r.currency,
            status: r.status,
            allows_overdraft: r.allows_overdraft,
            created_at: r.created_at,
        }))
    }

    /// Saldo materializado (lectura rápida, mantenido transaccionalmente).
    pub async fn balance(&self, account_id: Uuid) -> Result<Balance, sqlx::Error> {
        let row = sqlx::query!(
            "SELECT balance_minor, entry_count FROM account_balances WHERE account_id = $1",
            account_id,
        )
        .fetch_optional(&self.pool)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?;

        Ok(Balance { balance_minor: row.balance_minor, entry_count: row.entry_count })
    }

    pub async fn balance_minor(&self, account_id: Uuid) -> Result<i64, sqlx::Error> {
        Ok(self.balance(account_id).await?.balance_minor)
    }

    /// Saldo recalculado desde los asientos. Debe coincidir siempre con
    /// [`AccountRepository::balance`]; discrepancia = incidente contable.
    pub async fn projected_balance(&self, account_id: Uuid) -> Result<Balance, sqlx::Error> {
        let row = sqlx::query!(
            r#"
            SELECT projected_minor as "projected_minor!", projected_entries as "projected_entries!"
            FROM account_balance_projection WHERE account_id = $1
            "#,
            account_id,
        )
        .fetch_optional(&self.pool)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?;

        Ok(Balance { balance_minor: row.projected_minor, entry_count: row.projected_entries })
    }

    #[allow(clippy::too_many_arguments)]
    async fn insert(
        &self,
        code: &str,
        name: &str,
        account_type: AccountType,
        owner: AccountOwner,
        owner_id: Option<Uuid>,
        product_id: Option<Uuid>,
        currency: &str,
        allows_overdraft: bool,
    ) -> Result<Account, PostingError> {
        let r = sqlx::query!(
            r#"
            INSERT INTO accounts (code, name, type, owner, owner_id, product_id,
                                  currency, allows_overdraft)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
            RETURNING id, code, name,
                      type   as "account_type: AccountType",
                      owner  as "owner: AccountOwner",
                      owner_id, product_id, currency,
                      status as "status: AccountStatus",
                      allows_overdraft, created_at
            "#,
            code,
            name,
            account_type as AccountType,
            owner as AccountOwner,
            owner_id,
            product_id,
            currency,
            allows_overdraft,
        )
        .fetch_one(&self.pool)
        .await
        .map_err(PostingError::from_db)?;

        Ok(Account {
            id: r.id,
            code: r.code,
            name: r.name,
            account_type: r.account_type,
            owner: r.owner,
            owner_id: r.owner_id,
            product_id: r.product_id,
            currency: r.currency,
            status: r.status,
            allows_overdraft: r.allows_overdraft,
            created_at: r.created_at,
        })
    }
}

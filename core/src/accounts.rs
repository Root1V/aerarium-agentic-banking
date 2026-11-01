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

    /// Abre un bolsillo de una cuenta: una cuenta hija, del mismo cliente y bajo el
    /// mismo producto. Las reglas las hace cumplir la base (V012): mismo cliente,
    /// moneda y producto, un solo nivel, y la madre activa.
    pub async fn open_pocket(
        &self,
        parent_id: Uuid,
        code: &str,
        name: &str,
    ) -> Result<Account, PostingError> {
        let parent = self
            .find_by_id(parent_id)
            .await?
            .ok_or_else(|| PostingError::Invalid(format!("account {parent_id} not found")))?;
        let r = sqlx::query!(
            r#"
            INSERT INTO accounts (code, name, type, owner, owner_id, product_id,
                                  currency, allows_overdraft, parent_account_id)
            VALUES ($1, $2, $3, $4, $5, $6, $7, false, $8)
            RETURNING id, code, name,
                      type   as "account_type: AccountType",
                      owner  as "owner: AccountOwner",
                      owner_id, product_id, currency,
                      status as "status: AccountStatus",
                      allows_overdraft, created_at, parent_account_id
            "#,
            code,
            name,
            parent.account_type as AccountType,
            parent.owner as AccountOwner,
            parent.owner_id,
            parent.product_id,
            parent.currency,
            parent_id,
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
            parent_account_id: r.parent_account_id,
        })
    }

    /// Cierra un bolsillo vacío. Con saldo no se cierra: el dinero se devuelve
    /// antes a la cuenta, y así cerrar nunca hace desaparecer nada.
    pub async fn close_pocket(&self, pocket_id: Uuid) -> Result<Account, PostingError> {
        let mut tx = self.pool.begin().await?;
        let row = sqlx::query!(
            r#"
            SELECT a.parent_account_id, b.balance_micros
              FROM accounts a JOIN account_balances b ON b.account_id = a.id
             WHERE a.id = $1
               FOR UPDATE OF b
            "#,
            pocket_id,
        )
        .fetch_optional(&mut *tx)
        .await?
        .ok_or_else(|| PostingError::Invalid(format!("pocket {pocket_id} not found")))?;
        if row.parent_account_id.is_none() {
            return Err(PostingError::Invalid(format!(
                "account {pocket_id} is not a pocket"
            )));
        }
        if row.balance_micros != 0 {
            return Err(PostingError::Invalid(format!(
                "pocket {pocket_id} still holds {}; move it back first",
                row.balance_micros
            )));
        }
        sqlx::query!(
            "UPDATE accounts SET status = 'CLOSED' WHERE id = $1",
            pocket_id
        )
        .execute(&mut *tx)
        .await?;
        tx.commit().await?;
        self.find_by_id(pocket_id)
            .await?
            .ok_or_else(|| PostingError::Invalid(format!("pocket {pocket_id} not found")))
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
        self.insert(
            code,
            name,
            account_type,
            AccountOwner::Internal,
            None,
            None,
            currency,
            true,
        )
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
                   allows_overdraft, created_at, parent_account_id
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
            parent_account_id: r.parent_account_id,
        }))
    }

    /// Las cuentas de un cliente, de la más antigua a la más nueva.
    ///
    /// Es lo que necesita un login real: la persona dice quién es con su passkey,
    /// no qué cuenta quiere ver. Solo cuentas de cliente: las internas del banco
    /// no tienen titular y no deben aparecer nunca aquí.
    pub async fn list_for_customer(&self, customer_id: Uuid) -> Result<Vec<Account>, sqlx::Error> {
        let rows = sqlx::query!(
            r#"
            SELECT id, code, name,
                   type   as "account_type: AccountType",
                   owner  as "owner: AccountOwner",
                   owner_id, product_id, currency,
                   status as "status: AccountStatus",
                   allows_overdraft, created_at, parent_account_id
            FROM accounts
            WHERE owner = 'CUSTOMER' AND owner_id = $1
            ORDER BY created_at, id
            "#,
            customer_id,
        )
        .fetch_all(&self.pool)
        .await?;

        Ok(rows
            .into_iter()
            .map(|r| Account {
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
                parent_account_id: r.parent_account_id,
            })
            .collect())
    }

    pub async fn find_by_id(&self, id: Uuid) -> Result<Option<Account>, sqlx::Error> {
        let row = sqlx::query!(
            r#"
            SELECT id, code, name,
                   type   as "account_type: AccountType",
                   owner  as "owner: AccountOwner",
                   owner_id, product_id, currency,
                   status as "status: AccountStatus",
                   allows_overdraft, created_at, parent_account_id
            FROM accounts WHERE id = $1
            "#,
            id,
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
            parent_account_id: r.parent_account_id,
        }))
    }

    /// Saldo materializado (lectura rápida, mantenido transaccionalmente).
    pub async fn balance(&self, account_id: Uuid) -> Result<Balance, sqlx::Error> {
        let row = sqlx::query!(
            "SELECT balance_micros, entry_count FROM account_balances WHERE account_id = $1",
            account_id,
        )
        .fetch_optional(&self.pool)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?;

        Ok(Balance {
            balance_micros: row.balance_micros,
            entry_count: row.entry_count,
        })
    }

    pub async fn balance_micros(&self, account_id: Uuid) -> Result<i64, sqlx::Error> {
        Ok(self.balance(account_id).await?.balance_micros)
    }

    /// Extracto de movimientos, del más reciente al más antiguo.
    ///
    /// Pagina por el id del asiento, que es monotónico: a diferencia de OFFSET,
    /// un cursor no se descoloca cuando llegan movimientos nuevos mientras el
    /// cliente pasa de página.
    pub async fn list_entries(
        &self,
        account_id: Uuid,
        limit: i64,
        before: Option<i64>,
    ) -> Result<Vec<AccountEntry>, sqlx::Error> {
        let rows = sqlx::query!(
            r#"
            SELECT e.id, e.transaction_id,
                   e.direction as "direction: Direction",
                   e.amount_micros, e.currency,
                   t.kind, t.description, t.posted_at,
                   cp.id as "counterparty_id?", cp.name as "counterparty_name?"
            FROM ledger_entries e
            JOIN ledger_transactions t ON t.id = e.transaction_id
            -- La otra persona, solo si la transacción es de exactamente dos
            -- asientos y el otro es la cuenta de un cliente.
            LEFT JOIN LATERAL (
                SELECT a.id, a.name
                FROM ledger_entries o
                JOIN accounts a ON a.id = o.account_id
                WHERE o.transaction_id = e.transaction_id
                  AND o.account_id <> e.account_id
                  AND a.owner = 'CUSTOMER'
                  AND (SELECT count(*) FROM ledger_entries c WHERE c.transaction_id = e.transaction_id) = 2
                LIMIT 1
            ) cp ON true
            WHERE e.account_id = $1 AND ($2::bigint IS NULL OR e.id < $2)
            ORDER BY e.id DESC
            LIMIT $3
            "#,
            account_id,
            before,
            limit,
        )
        .fetch_all(&self.pool)
        .await?;

        Ok(rows
            .into_iter()
            .map(|r| AccountEntry {
                id: r.id,
                transaction_id: r.transaction_id,
                direction: r.direction,
                amount_micros: r.amount_micros,
                currency: r.currency,
                kind: r.kind,
                description: r.description,
                posted_at: r.posted_at,
                counterparty: match (r.counterparty_id, r.counterparty_name) {
                    (Some(account_id), Some(name)) => Some(Counterparty { account_id, name }),
                    _ => None,
                },
            })
            .collect())
    }

    /// Saldo recalculado desde los asientos. Debe coincidir siempre con
    /// [`AccountRepository::balance`]; discrepancia = incidente contable.
    pub async fn projected_balance(&self, account_id: Uuid) -> Result<Balance, sqlx::Error> {
        let row = sqlx::query!(
            r#"
            SELECT projected_micros as "projected_micros!", projected_entries as "projected_entries!"
            FROM account_balance_projection WHERE account_id = $1
            "#,
            account_id,
        )
        .fetch_optional(&self.pool)
        .await?
        .ok_or(sqlx::Error::RowNotFound)?;

        Ok(Balance {
            balance_micros: row.projected_micros,
            entry_count: row.projected_entries,
        })
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
                      allows_overdraft, created_at, parent_account_id
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
            parent_account_id: r.parent_account_id,
        })
    }
}

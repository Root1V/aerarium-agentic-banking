//! Utilidades compartidas por los tests de integración.
//!
//! Requiere Postgres: `docker compose -f platform/docker-compose.yml up -d`

#![allow(dead_code)] // cada binario de test usa un subconjunto de estos helpers
#![allow(clippy::inconsistent_digit_grouping)]

use aibank_core::{
    db, AccountRepository, AccountType, Direction, EntryCommand, NewProduct, PostingRequest,
    PostingService, Product, ProductRepository,
};
use sqlx::PgPool;
use uuid::Uuid;

pub struct Ctx {
    pub pool: PgPool,
    pub accounts: AccountRepository,
    pub products: ProductRepository,
    pub posting: PostingService,
}

/// Construye el contexto sobre un pool ya existente (para `#[sqlx::test]`,
/// que entrega una base aislada por test).
pub fn from_pool(pool: PgPool) -> Ctx {
    Ctx {
        accounts: AccountRepository::new(pool.clone()),
        products: ProductRepository::new(pool.clone()),
        posting: PostingService::new(pool.clone()),
        pool,
    }
}

pub async fn setup() -> Ctx {
    let url = std::env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://aibank:aibank_dev@localhost:5434/aibank".to_string());
    let pool = db::connect(&url, 24).await.expect("connect to postgres");
    db::migrate(&pool).await.expect("run migrations");

    Ctx {
        accounts: AccountRepository::new(pool.clone()),
        products: ProductRepository::new(pool.clone()),
        posting: PostingService::new(pool.clone()),
        pool,
    }
}

impl Ctx {
    /// Producto de cuenta simple, con topes opcionales.
    pub async fn simple_product(
        &self,
        max_balance_micros: Option<i64>,
        max_transaction_micros: Option<i64>,
    ) -> Product {
        let code = format!("SIMPLE-{}", Uuid::new_v4());
        self.products
            .create(
                &NewProduct::deposit_account(code, "Cuenta simple", "USD")
                    .with_caps(max_balance_micros, max_transaction_micros),
            )
            .await
            .expect("create product")
    }

    /// Caja interna (ASSET) + cuenta de cliente (LIABILITY) sobre `product`.
    pub async fn cash_and_customer(&self, product: &Product) -> (Uuid, Uuid) {
        let suffix = Uuid::new_v4();
        let cash = self
            .accounts
            .create_internal(&format!("cash-{suffix}"), "Caja operativa", AccountType::Asset, "USD")
            .await
            .expect("create cash account");
        let customer = self
            .accounts
            .open_customer_account(
                &format!("cust-{suffix}"),
                "Cliente",
                Uuid::new_v4(),
                product,
            )
            .await
            .expect("open customer account");
        (cash.id, customer.id)
    }

    /// Cuenta de cliente adicional sobre el mismo producto.
    pub async fn customer_account(&self, product: &Product) -> Uuid {
        self.accounts
            .open_customer_account(
                &format!("cust-{}", Uuid::new_v4()),
                "Cliente",
                Uuid::new_v4(),
                product,
            )
            .await
            .expect("open customer account")
            .id
    }
}

/// Depósito: entra dinero al banco (debe en caja) y nace el saldo del cliente (haber).
pub fn deposit(cash: Uuid, customer: Uuid, amount: i64, key: &str) -> PostingRequest {
    PostingRequest::new(
        key,
        "deposit",
        vec![
            EntryCommand::debit(cash, amount, "USD"),
            EntryCommand::credit(customer, amount, "USD"),
        ],
    )
}

/// Retiro: se reduce el saldo del cliente (debe) y sale dinero de caja (haber).
pub fn withdrawal(cash: Uuid, customer: Uuid, amount: i64, key: &str) -> PostingRequest {
    PostingRequest::new(
        key,
        "withdrawal",
        vec![
            EntryCommand::debit(customer, amount, "USD"),
            EntryCommand::credit(cash, amount, "USD"),
        ],
    )
}

pub fn transfer(from: Uuid, to: Uuid, amount: i64, key: &str) -> PostingRequest {
    PostingRequest::new(
        key,
        "p2p_transfer",
        vec![
            EntryCommand { account_id: from, direction: Direction::Debit, amount_micros: amount, currency: "USD".into() },
            EntryCommand { account_id: to, direction: Direction::Credit, amount_micros: amount, currency: "USD".into() },
        ],
    )
}

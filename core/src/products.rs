//! Catálogo de productos.
//!
//! Un producto es configuración, no código: define moneda, si admite sobregiro y
//! los topes regulatorios. Lanzar un producto nuevo es insertar una fila.

use crate::model::*;
use sqlx::PgPool;

pub struct ProductRepository {
    pool: PgPool,
}

impl ProductRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    pub async fn create(&self, product: &NewProduct) -> Result<Product, PostingError> {
        if product.currency.len() != 3 {
            return Err(PostingError::Invalid(format!(
                "currency must be ISO-4217, got '{}'",
                product.currency
            )));
        }
        if product.max_balance_micros.is_some_and(|cap| cap <= 0) {
            return Err(PostingError::Invalid("max_balance_micros must be positive".into()));
        }
        if product.max_transaction_micros.is_some_and(|cap| cap <= 0) {
            return Err(PostingError::Invalid("max_transaction_micros must be positive".into()));
        }

        let r = sqlx::query!(
            r#"
            INSERT INTO products (code, name, kind, currency, allows_overdraft,
                                  max_balance_micros, max_transaction_micros)
            VALUES ($1, $2, $3, $4, $5, $6, $7)
            RETURNING id, code, name, kind as "kind: ProductKind", currency,
                      allows_overdraft, max_balance_micros, max_transaction_micros, active
            "#,
            product.code,
            product.name,
            product.kind as ProductKind,
            product.currency,
            product.allows_overdraft,
            product.max_balance_micros,
            product.max_transaction_micros,
        )
        .fetch_one(&self.pool)
        .await
        .map_err(PostingError::from_db)?;

        Ok(Product {
            id: r.id,
            code: r.code,
            name: r.name,
            kind: r.kind,
            currency: r.currency,
            allows_overdraft: r.allows_overdraft,
            max_balance_micros: r.max_balance_micros,
            max_transaction_micros: r.max_transaction_micros,
            active: r.active,
        })
    }

    pub async fn find_by_code(&self, code: &str) -> Result<Option<Product>, sqlx::Error> {
        let row = sqlx::query!(
            r#"
            SELECT id, code, name, kind as "kind: ProductKind", currency,
                   allows_overdraft, max_balance_micros, max_transaction_micros, active
            FROM products WHERE code = $1
            "#,
            code,
        )
        .fetch_optional(&self.pool)
        .await?;

        Ok(row.map(|r| Product {
            id: r.id,
            code: r.code,
            name: r.name,
            kind: r.kind,
            currency: r.currency,
            allows_overdraft: r.allows_overdraft,
            max_balance_micros: r.max_balance_micros,
            max_transaction_micros: r.max_transaction_micros,
            active: r.active,
        }))
    }
}

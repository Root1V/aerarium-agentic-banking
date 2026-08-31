//! Motor de posting: la ÚNICA vía de escritura al ledger.
//!
//! Garantías:
//! - Validación de negocio antes de tocar la base (2+ asientos, montos > 0, balance por moneda).
//! - Idempotencia: la misma clave produce exactamente una transacción; un reintento
//!   devuelve la original. La misma clave con payload distinto es un error explícito.
//! - Atomicidad: transacción y asientos se escriben en una sola transacción de base de
//!   datos; los triggers del esquema re-verifican las invariantes al COMMIT.

use crate::model::*;
use sha2::{Digest, Sha256};
use sqlx::{PgPool, Postgres, Transaction};
use std::collections::BTreeMap;
use uuid::Uuid;

pub struct PostingService {
    pool: PgPool,
}

impl PostingService {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    pub async fn post(&self, request: &PostingRequest) -> Result<PostingResult, PostingError> {
        validate(request)?;
        let hash = request_hash(request);

        let mut tx = self.pool.begin().await.map_err(PostingError::from_db)?;

        let result = match insert_transaction(&mut tx, request, &hash)
            .await
            .map_err(PostingError::from_db)?
        {
            Some(transaction) => {
                insert_entries(&mut tx, transaction.id, &request.entries)
                    .await
                    .map_err(PostingError::from_db)?;
                PostingResult { transaction, replayed: false }
            }
            None => {
                // La clave ya existe. `ON CONFLICT DO NOTHING` espera a que la transacción
                // concurrente dueña de la clave termine, así que aquí ya es visible.
                let (transaction, existing_hash) = find_by_key(&mut tx, &request.idempotency_key)
                    .await
                    .map_err(PostingError::from_db)?
                    .ok_or_else(|| {
                        PostingError::Database(sqlx::Error::Protocol(
                            "idempotency key vanished after conflict; concurrent rollback".into(),
                        ))
                    })?;
                if existing_hash != hash {
                    return Err(PostingError::IdempotencyConflict(request.idempotency_key.clone()));
                }
                PostingResult { transaction, replayed: true }
            }
        };

        // Aquí se evalúan los triggers diferidos: desbalance, sobregiro y topes del
        // producto abortan el COMMIT.
        tx.commit().await.map_err(PostingError::from_db)?;
        Ok(result)
    }
}

// ---------------------------------------------------------------- validación

fn validate(request: &PostingRequest) -> Result<(), PostingError> {
    if request.idempotency_key.trim().is_empty() {
        return Err(PostingError::Invalid("idempotency_key must not be blank".into()));
    }
    if request.kind.trim().is_empty() {
        return Err(PostingError::Invalid("kind must not be blank".into()));
    }
    if request.entries.len() < 2 {
        return Err(PostingError::Invalid("a transaction requires at least 2 entries".into()));
    }

    for entry in &request.entries {
        if entry.amount_minor <= 0 {
            return Err(PostingError::Invalid(format!(
                "amounts must be positive, got {}",
                entry.amount_minor
            )));
        }
        if entry.currency.len() != 3 {
            return Err(PostingError::Invalid(format!(
                "currency must be ISO-4217, got '{}'",
                entry.currency
            )));
        }
    }

    let mut net_by_currency: BTreeMap<&str, i128> = BTreeMap::new();
    for entry in &request.entries {
        let signed = match entry.direction {
            Direction::Debit => entry.amount_minor as i128,
            Direction::Credit => -(entry.amount_minor as i128),
        };
        *net_by_currency.entry(entry.currency.as_str()).or_default() += signed;
    }
    for (currency, net) in net_by_currency {
        if net != 0 {
            return Err(PostingError::Invalid(format!(
                "unbalanced transaction for {currency}: debits minus credits = {net}"
            )));
        }
    }

    Ok(())
}

/// Hash canónico del payload, para detectar reutilización de clave con contenido distinto.
fn request_hash(request: &PostingRequest) -> String {
    let mut parts: Vec<String> = request
        .entries
        .iter()
        .map(|e| {
            format!(
                "{}:{:?}:{}:{}",
                e.account_id, e.direction, e.amount_minor, e.currency
            )
        })
        .collect();
    parts.sort();

    let mut hasher = Sha256::new();
    hasher.update(request.kind.as_bytes());
    hasher.update(b"|");
    for part in parts {
        hasher.update(part.as_bytes());
        hasher.update(b";");
    }
    format!("{:x}", hasher.finalize())
}

// ---------------------------------------------------------------- persistencia

async fn insert_transaction(
    tx: &mut Transaction<'_, Postgres>,
    request: &PostingRequest,
    hash: &str,
) -> Result<Option<LedgerTransaction>, sqlx::Error> {
    let row = sqlx::query!(
        r#"
        INSERT INTO ledger_transactions (idempotency_key, request_hash, kind, description)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (idempotency_key) DO NOTHING
        RETURNING id, idempotency_key, kind, description, posted_at
        "#,
        request.idempotency_key,
        hash,
        request.kind,
        request.description.as_deref(),
    )
    .fetch_optional(&mut **tx)
    .await?;

    Ok(row.map(|r| LedgerTransaction {
        id: r.id,
        idempotency_key: r.idempotency_key,
        kind: r.kind,
        description: r.description,
        posted_at: r.posted_at,
    }))
}

async fn insert_entries(
    tx: &mut Transaction<'_, Postgres>,
    transaction_id: Uuid,
    entries: &[EntryCommand],
) -> Result<(), sqlx::Error> {
    // Insertar en orden determinista por cuenta: el trigger de saldo toma lock de
    // fila, y un orden global fijo evita deadlocks entre transferencias cruzadas
    // (A→B y B→A simultáneas bloquearían en orden inverso sin esto).
    let mut ordered: Vec<&EntryCommand> = entries.iter().collect();
    ordered.sort_by_key(|e| (e.account_id, e.direction as i32, e.amount_minor));

    for entry in ordered {
        sqlx::query!(
            r#"
            INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_minor, currency)
            VALUES ($1, $2, $3, $4, $5)
            "#,
            transaction_id,
            entry.account_id,
            entry.direction as Direction,
            entry.amount_minor,
            entry.currency,
        )
        .execute(&mut **tx)
        .await?;
    }
    Ok(())
}

async fn find_by_key(
    tx: &mut Transaction<'_, Postgres>,
    key: &str,
) -> Result<Option<(LedgerTransaction, String)>, sqlx::Error> {
    let row = sqlx::query!(
        r#"
        SELECT id, idempotency_key, kind, description, posted_at, request_hash
        FROM ledger_transactions
        WHERE idempotency_key = $1
        "#,
        key,
    )
    .fetch_optional(&mut **tx)
    .await?;

    Ok(row.map(|r| {
        (
            LedgerTransaction {
                id: r.id,
                idempotency_key: r.idempotency_key,
                kind: r.kind,
                description: r.description,
                posted_at: r.posted_at,
            },
            r.request_hash,
        )
    }))
}

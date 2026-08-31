//! Outbox transaccional y su relay.
//!
//! El evento se escribe en la misma transacción que los asientos ([`write`]), y un
//! proceso aparte lo publica ([`Relay`]). Ver `migrations/0003_outbox.sql` para el
//! razonamiento sobre por qué no se publica directamente desde el posting.

use prost::Message;
use sqlx::{PgPool, Postgres, Transaction};
use std::time::Duration;
use uuid::Uuid;

pub mod pb {
    tonic::include_proto!("aibank.events.v1");
}

/// Mensaje pendiente de publicar.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OutboxMessage {
    pub id: i64,
    pub event_id: Uuid,
    pub event_type: String,
    pub aggregate_type: String,
    /// Clave de partición en el bus: garantiza orden por agregado.
    pub aggregate_id: Uuid,
    pub payload: Vec<u8>,
}

/// Encola un evento dentro de una transacción en curso.
///
/// Recibe la transacción por parámetro justamente para que no exista forma de
/// emitir un evento fuera de la transacción que lo originó.
pub(crate) async fn write(
    tx: &mut Transaction<'_, Postgres>,
    event_id: Uuid,
    event_type: &str,
    aggregate_type: &str,
    aggregate_id: Uuid,
    payload: &impl Message,
) -> Result<(), sqlx::Error> {
    sqlx::query!(
        r#"
        INSERT INTO outbox (event_id, event_type, aggregate_type, aggregate_id, payload)
        VALUES ($1, $2, $3, $4, $5)
        "#,
        event_id,
        event_type,
        aggregate_type,
        aggregate_id,
        payload.encode_to_vec(),
    )
    .execute(&mut **tx)
    .await?;
    Ok(())
}

// ---------------------------------------------------------------- publicador

#[derive(Debug, thiserror::Error)]
#[error("publish failed: {0}")]
pub struct PublishError(pub String);

/// Destino de los eventos. Abstraerlo permite correr en local sin bus y cambiar
/// de transporte sin tocar la lógica de entrega.
#[async_trait::async_trait]
pub trait Publisher: Send + Sync {
    async fn publish(&self, message: &OutboxMessage) -> Result<(), PublishError>;
}

/// Publicador de desarrollo: registra el evento en el log.
pub struct LoggingPublisher;

#[async_trait::async_trait]
impl Publisher for LoggingPublisher {
    async fn publish(&self, message: &OutboxMessage) -> Result<(), PublishError> {
        tracing::info!(
            id = message.id,
            event_id = %message.event_id,
            event_type = %message.event_type,
            aggregate_id = %message.aggregate_id,
            bytes = message.payload.len(),
            "evento publicado (logging)"
        );
        Ok(())
    }
}

// ---------------------------------------------------------------- relay

/// Publica los eventos pendientes en orden y los marca.
///
/// Varias instancias pueden correr a la vez: el `FOR UPDATE SKIP LOCKED` reparte
/// los lotes sin que dos relays tomen el mismo mensaje.
pub struct Relay {
    pool: PgPool,
    publisher: Box<dyn Publisher>,
    batch_size: i64,
}

impl Relay {
    pub fn new(pool: PgPool, publisher: Box<dyn Publisher>) -> Self {
        Self { pool, publisher, batch_size: 100 }
    }

    pub fn with_batch_size(mut self, batch_size: i64) -> Self {
        self.batch_size = batch_size;
        self
    }

    /// Procesa un lote. Devuelve cuántos eventos se publicaron.
    ///
    /// Un fallo de publicación no aborta el lote: se registra el intento y el
    /// evento queda pendiente para la próxima pasada. Los eventos que sí se
    /// publicaron se marcan igualmente.
    pub async fn run_once(&self) -> Result<usize, sqlx::Error> {
        let mut tx = self.pool.begin().await?;

        let rows = sqlx::query!(
            r#"
            SELECT id, event_id, event_type, aggregate_type, aggregate_id, payload
            FROM outbox
            WHERE published_at IS NULL
            ORDER BY id
            LIMIT $1
            FOR UPDATE SKIP LOCKED
            "#,
            self.batch_size,
        )
        .fetch_all(&mut *tx)
        .await?;

        if rows.is_empty() {
            tx.commit().await?;
            return Ok(0);
        }

        let mut published = Vec::new();
        for row in rows {
            let message = OutboxMessage {
                id: row.id,
                event_id: row.event_id,
                event_type: row.event_type,
                aggregate_type: row.aggregate_type,
                aggregate_id: row.aggregate_id,
                payload: row.payload,
            };

            match self.publisher.publish(&message).await {
                Ok(()) => published.push(message.id),
                Err(err) => {
                    // Se detiene el lote en el primer fallo para no romper el
                    // orden por agregado: el resto se reintenta en la próxima pasada.
                    sqlx::query!(
                        "UPDATE outbox SET attempts = attempts + 1, last_error = $2 WHERE id = $1",
                        message.id,
                        err.to_string(),
                    )
                    .execute(&mut *tx)
                    .await?;
                    tracing::warn!(id = message.id, error = %err, "fallo al publicar");
                    break;
                }
            }
        }

        if !published.is_empty() {
            sqlx::query!(
                "UPDATE outbox SET published_at = now() WHERE id = ANY($1)",
                &published[..],
            )
            .execute(&mut *tx)
            .await?;
        }

        tx.commit().await?;
        Ok(published.len())
    }

    /// Bucle continuo: consume la cola y espera cuando se vacía.
    pub async fn run(&self, idle_interval: Duration) -> Result<(), sqlx::Error> {
        loop {
            let published = self.run_once().await?;
            if published == 0 {
                tokio::time::sleep(idle_interval).await;
            }
        }
    }
}

/// Cuenta los eventos aún sin publicar. Métrica de salud del relay: si crece de
/// forma sostenida, el bus o el relay están caídos.
pub async fn pending_count(pool: &PgPool) -> Result<i64, sqlx::Error> {
    let row = sqlx::query!(r#"SELECT COUNT(*) as "count!" FROM outbox WHERE published_at IS NULL"#)
        .fetch_one(pool)
        .await?;
    Ok(row.count)
}

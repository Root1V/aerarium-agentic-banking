//! Relay del outbox: publica en el bus los eventos ya comprometidos en la base.
//!
//! Variables de entorno:
//!   DATABASE_URL    conexión a PostgreSQL
//!   KAFKA_BROKERS   brokers; si está vacío, publica al log (modo desarrollo)
//!   KAFKA_TOPIC     tópico destino (por defecto aibank.ledger.v1)

use aibank_core::db;
use aibank_core::outbox::{LoggingPublisher, Publisher, Relay};
use std::error::Error;
use std::time::Duration;

#[tokio::main]
async fn main() -> Result<(), Box<dyn Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
        )
        .init();

    let database_url = std::env::var("DATABASE_URL")
        .unwrap_or_else(|_| "postgres://aibank:aibank_dev@localhost:5434/aibank".to_string());
    let brokers = std::env::var("KAFKA_BROKERS").unwrap_or_default();
    let topic = std::env::var("KAFKA_TOPIC").unwrap_or_else(|_| "aibank.ledger.v1".to_string());

    let pool = db::connect(&database_url, 8).await?;

    let publisher: Box<dyn Publisher> = if brokers.is_empty() {
        tracing::warn!("KAFKA_BROKERS vacío: publicando al log (solo desarrollo)");
        Box::new(LoggingPublisher)
    } else {
        tracing::info!(%brokers, %topic, "publicando en Kafka");
        Box::new(aibank_core::kafka::KafkaPublisher::new(&brokers, topic)?)
    };

    let relay = Relay::new(pool, publisher);
    tracing::info!("relay iniciado");
    relay.run(Duration::from_millis(500)).await?;

    Ok(())
}

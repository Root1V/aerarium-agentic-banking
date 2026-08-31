//! Publicador Kafka del outbox.
//!
//! Configuración deliberada para banca:
//! - `acks=all`: el broker confirma solo cuando todas las réplicas escribieron.
//!   Con `acks=1` un failover del líder pierde eventos ya confirmados.
//! - `enable.idempotence=true`: evita que un reintento interno del productor
//!   duplique el mensaje en el broker.
//! - Clave del mensaje = `aggregate_id`: garantiza orden por agregado, ya que
//!   Kafka solo ordena dentro de una partición.

use crate::outbox::{OutboxMessage, PublishError, Publisher};
use rdkafka::config::ClientConfig;
use rdkafka::producer::{FutureProducer, FutureRecord};
use std::time::Duration;

pub struct KafkaPublisher {
    producer: FutureProducer,
    topic: String,
    timeout: Duration,
}

impl KafkaPublisher {
    pub fn new(brokers: &str, topic: impl Into<String>) -> Result<Self, PublishError> {
        let producer: FutureProducer = ClientConfig::new()
            .set("bootstrap.servers", brokers)
            .set("acks", "all")
            .set("enable.idempotence", "true")
            .set("message.timeout.ms", "10000")
            .set("compression.type", "lz4")
            .create()
            .map_err(|e| PublishError(format!("crear productor: {e}")))?;

        Ok(Self { producer, topic: topic.into(), timeout: Duration::from_secs(10) })
    }
}

#[async_trait::async_trait]
impl Publisher for KafkaPublisher {
    async fn publish(&self, message: &OutboxMessage) -> Result<(), PublishError> {
        let key = message.aggregate_id.to_string();
        let event_id = message.event_id.to_string();

        let mut headers = rdkafka::message::OwnedHeaders::new();
        headers = headers.insert(rdkafka::message::Header {
            key: "event_id",
            value: Some(&event_id),
        });
        headers = headers.insert(rdkafka::message::Header {
            key: "event_type",
            value: Some(&message.event_type),
        });

        let record = FutureRecord::to(&self.topic)
            .key(&key)
            .payload(&message.payload)
            .headers(headers);

        self.producer
            .send(record, self.timeout)
            .await
            .map_err(|(e, _)| PublishError(format!("enviar a Kafka: {e}")))?;

        Ok(())
    }
}

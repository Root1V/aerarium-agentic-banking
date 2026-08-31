//! Trazado distribuido: propagación entre procesos y a través del tiempo.
//!
//! El caso difícil no es pasar el contexto de una llamada a otra, sino que
//! sobreviva al salto asíncrono: el evento del outbox se publica minutos después
//! y en otro proceso. Si la traza se corta ahí, seguir un pago de extremo a
//! extremo deja de ser posible justo donde más falta hace.

#![allow(clippy::inconsistent_digit_grouping)]

mod common;

use aibank_core::telemetry;
use common::*;
use opentelemetry::trace::{TraceContextExt, TraceId};
use opentelemetry::Context;
use sqlx::PgPool;
use tonic::metadata::MetadataMap;
use uuid::Uuid;

/// Contexto con una traza conocida, como el que enviaría un servicio llamante.
fn context_with_trace(traceparent: &str) -> Context {
    telemetry::deserialize_context(traceparent)
}

const SAMPLE: &str = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";
const SAMPLE_TRACE_ID: &str = "4bf92f3577b34da6a3ce929d0e0e4736";

// ---------------------------------------------------------------- propagación

#[test]
fn el_contexto_viaja_por_la_metadata_grpc() {
    let mut metadata = MetadataMap::new();
    metadata.insert("traceparent", SAMPLE.parse().unwrap());

    let context = telemetry::context_from_metadata(&metadata);

    assert_eq!(
        telemetry::trace_id_of(&context).as_deref(),
        Some(SAMPLE_TRACE_ID),
        "el core debe adoptar la traza del llamador, no empezar una nueva"
    );
}

#[test]
fn una_llamada_sin_contexto_no_falla() {
    // La telemetría nunca debe tumbar una operación de dinero.
    let context = telemetry::context_from_metadata(&MetadataMap::new());
    assert_eq!(telemetry::trace_id_of(&context), None);
}

#[test]
fn el_contexto_sobrevive_a_una_ida_y_vuelta_por_metadata() {
    let original = context_with_trace(SAMPLE);
    let mut metadata = MetadataMap::new();
    telemetry::inject_into_metadata(&original, &mut metadata);

    let recovered = telemetry::context_from_metadata(&metadata);

    assert_eq!(
        telemetry::trace_id_of(&recovered),
        telemetry::trace_id_of(&original)
    );
    assert_ne!(telemetry::trace_id_of(&recovered), None);
}

#[test]
fn un_traceparent_invalido_se_ignora_en_vez_de_romper() {
    let context = telemetry::deserialize_context("esto-no-es-un-traceparent");
    assert_eq!(telemetry::trace_id_of(&context), None);
}

// ---------------------------------------------------------------- salto asíncrono

// La propiedad central: el evento conserva la traza de la operación que lo generó.
#[sqlx::test]
async fn el_evento_del_outbox_conserva_la_traza_de_su_origen(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let origin = context_with_trace(SAMPLE);
    let _guard = origin.attach();

    let result = ctx
        .posting
        .post(&deposit(cash, customer, 120_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    let stored: Option<String> = sqlx::query_scalar(
        "SELECT trace_context FROM outbox WHERE aggregate_id = $1",
    )
    .bind(result.transaction.id)
    .fetch_one(&pool)
    .await
    .unwrap();

    let traceparent = stored.expect("el evento debe guardar el contexto de traza");
    let recovered = telemetry::deserialize_context(&traceparent);

    assert_eq!(
        telemetry::trace_id_of(&recovered).as_deref(),
        Some(SAMPLE_TRACE_ID),
        "la publicación posterior debe poder enlazarse con el pago que la originó"
    );
}

#[sqlx::test]
async fn sin_traza_activa_el_evento_se_asienta_igual(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    // Sin contexto: un proceso por lotes, una migración, una prueba.
    let result = ctx
        .posting
        .post(&deposit(cash, customer, 50_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .expect("la ausencia de traza no puede impedir mover dinero");

    let stored: Option<String> = sqlx::query_scalar(
        "SELECT trace_context FROM outbox WHERE aggregate_id = $1",
    )
    .bind(result.transaction.id)
    .fetch_one(&pool)
    .await
    .unwrap();

    assert_eq!(stored, None, "sin traza no se inventa un contexto");
}

#[sqlx::test]
async fn el_relay_recupera_la_traza_al_publicar(pool: PgPool) {
    use aibank_core::outbox::{OutboxMessage, PublishError, Publisher, Relay};
    use std::sync::{Arc, Mutex};

    struct Spy(Arc<Mutex<Vec<OutboxMessage>>>);

    #[async_trait::async_trait]
    impl Publisher for Spy {
        async fn publish(&self, message: &OutboxMessage) -> Result<(), PublishError> {
            self.0.lock().unwrap().push(message.clone());
            Ok(())
        }
    }

    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let origin = context_with_trace(SAMPLE);
    let _guard = origin.attach();
    ctx.posting
        .post(&deposit(cash, customer, 70_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();
    drop(_guard);

    let published = Arc::new(Mutex::new(Vec::new()));
    Relay::new(pool, Box::new(Spy(Arc::clone(&published))))
        .run_once()
        .await
        .unwrap();

    let messages = published.lock().unwrap();
    let message = messages.first().expect("debe publicarse el evento");
    let traceparent = message
        .trace_context
        .as_ref()
        .expect("el relay debe recibir el contexto junto al evento");

    assert_eq!(
        telemetry::trace_id_of(&telemetry::deserialize_context(traceparent)).as_deref(),
        Some(SAMPLE_TRACE_ID)
    );
}

// ---------------------------------------------------------------- guarda de PII

// Las trazas salen a herramientas de terceros y se conservan mucho tiempo.
#[test]
fn los_datos_personales_y_secretos_nunca_entran_en_una_traza() {
    for forbidden in [
        "pan",
        "card_number",
        "cvv",
        "document_number",
        "customer_email",
        "phone",
        "password",
        "auth_token",
        "api_key",
        "client_secret",
        // La comparación es por subcadena: una variante tampoco pasa.
        "CARD_NUMBER_HASH",
        "userEmail",
    ] {
        assert!(
            !telemetry::is_safe_attribute(forbidden),
            "'{forbidden}' no debería poder registrarse"
        );
    }
}

#[test]
fn los_identificadores_de_negocio_si_pueden_registrarse() {
    for allowed in [
        "transaction_id",
        "account_id",
        "idempotency_key",
        "amount_minor",
        "currency",
        "kind",
        "network_transaction_id",
    ] {
        assert!(
            telemetry::is_safe_attribute(allowed),
            "'{allowed}' debería poder registrarse: sin identificadores no hay traza útil"
        );
    }
}

#[test]
fn el_filtro_descarta_solo_lo_prohibido() {
    let filtered = telemetry::safe_attributes([
        ("transaction_id", "tx-1"),
        ("document_number", "12345678"),
        ("amount_minor", "12000"),
        ("password", "hunter2"),
    ]);

    let keys: Vec<_> = filtered.iter().map(|(k, _)| k.as_str()).collect();
    assert_eq!(keys, vec!["transaction_id", "amount_minor"]);
}

#[test]
fn una_traza_valida_tiene_identificador_utilizable() {
    let context = context_with_trace(SAMPLE);
    let trace_id = telemetry::trace_id_of(&context).unwrap();

    assert_eq!(trace_id.len(), 32, "el id debe poder pegarse en un buscador");
    assert_ne!(
        context.span().span_context().trace_id(),
        TraceId::INVALID
    );
}

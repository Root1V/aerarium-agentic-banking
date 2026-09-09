//! Garantías del outbox transaccional:
//!
//! 1. El evento se escribe en la MISMA transacción que los asientos.
//! 2. Si la transacción se rechaza, no queda evento (la propiedad que hace
//!    imposible la doble escritura).
//! 3. Un replay idempotente no emite un segundo evento.
//! 4. El evento lleva el saldo resultante de cada cuenta.
//! 5. El relay publica en orden, marca lo publicado y no republica.
//! 6. Si el publicador falla, el evento queda pendiente y se reintenta.

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::outbox::{pb, OutboxMessage, PublishError, Publisher, Relay};
use aibank_core::PostingError;
use common::*;
use sqlx::PgPool;
use prost::Message;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use uuid::Uuid;

/// Publicador de prueba: registra lo publicado y puede simular caída del bus.
#[derive(Default)]
struct SpyPublisher {
    published: Mutex<Vec<OutboxMessage>>,
    failing: AtomicBool,
}

impl SpyPublisher {
    fn published(&self) -> Vec<OutboxMessage> {
        self.published.lock().unwrap().clone()
    }
    fn set_failing(&self, failing: bool) {
        self.failing.store(failing, Ordering::SeqCst);
    }
}

#[async_trait::async_trait]
impl Publisher for SpyPublisher {
    async fn publish(&self, message: &OutboxMessage) -> Result<(), PublishError> {
        if self.failing.load(Ordering::SeqCst) {
            return Err(PublishError("bus caído".into()));
        }
        self.published.lock().unwrap().push(message.clone());
        Ok(())
    }
}

/// Publicador que delega en un spy compartido, para inspeccionarlo desde el test.
struct SharedPublisher(Arc<SpyPublisher>);

#[async_trait::async_trait]
impl Publisher for SharedPublisher {
    async fn publish(&self, message: &OutboxMessage) -> Result<(), PublishError> {
        self.0.publish(message).await
    }
}

/// Eventos pendientes de una transacción concreta.
async fn events_of(ctx: &Ctx, transaction_id: Uuid) -> Vec<pb::LedgerTransactionPosted> {
    let rows = sqlx::query_scalar::<_, Vec<u8>>(
        "SELECT payload FROM outbox WHERE aggregate_id = $1 ORDER BY id",
    )
    .bind(transaction_id)
    .fetch_all(&ctx.pool)
    .await
    .unwrap();

    rows.iter()
        .map(|bytes| pb::LedgerTransactionPosted::decode(&bytes[..]).expect("decodificar evento"))
        .collect()
}

async fn outbox_count_for_key(ctx: &Ctx, idempotency_key: &str) -> i64 {
    sqlx::query_scalar::<_, i64>(
        r#"
        SELECT COUNT(*) FROM outbox o
        JOIN ledger_transactions t ON t.id = o.aggregate_id
        WHERE t.idempotency_key = $1
        "#,
    )
    .bind(idempotency_key)
    .fetch_one(&ctx.pool)
    .await
    .unwrap()
}

// ------------------------------------------------------------- 1 y 4

#[sqlx::test]
async fn un_posting_exitoso_encola_su_evento_con_los_saldos_resultantes(pool: PgPool) {
    let ctx = from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let result = ctx
        .posting
        .post(&deposit(cash, customer, 250_000000, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    let events = events_of(&ctx, result.transaction.id).await;
    assert_eq!(events.len(), 1, "un posting emite exactamente un evento");

    let event = &events[0];
    assert_eq!(event.transaction_id, result.transaction.id.to_string());
    assert_eq!(event.kind, "deposit");
    assert_eq!(event.entries.len(), 2);

    let customer_entry = event
        .entries
        .iter()
        .find(|e| e.account_id == customer.to_string())
        .expect("el evento incluye el asiento del cliente");
    assert_eq!(customer_entry.balance_after_micros, 250_000000, "el evento lleva el saldo resultante");
    assert_eq!(customer_entry.amount.as_ref().unwrap().amount_micros, 250_000000);
    assert_eq!(customer_entry.amount.as_ref().unwrap().currency, "USD");
}

// ------------------------------------------------------------- 2

#[sqlx::test]
async fn una_transaccion_rechazada_no_deja_evento(pool: PgPool) {
    let ctx = from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting
        .post(&deposit(cash, customer, 100_000000, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    // El rechazo ocurre en el COMMIT (trigger diferido de sobregiro): si el evento
    // se hubiera publicado antes, ya sería irreversible.
    let key = format!("wd-{}", Uuid::new_v4());
    let result = ctx.posting.post(&withdrawal(cash, customer, 500_000000, &key)).await;
    assert!(matches!(result, Err(PostingError::InsufficientFunds(_))));

    assert_eq!(
        outbox_count_for_key(&ctx, &key).await,
        0,
        "el evento se revierte junto con los asientos"
    );
}

// ------------------------------------------------------------- 3

#[sqlx::test]
async fn un_replay_idempotente_no_emite_un_segundo_evento(pool: PgPool) {
    let ctx = from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let key = format!("rep-{}", Uuid::new_v4());

    let first = ctx.posting.post(&deposit(cash, customer, 40_000000, &key)).await.unwrap();
    let second = ctx.posting.post(&deposit(cash, customer, 40_000000, &key)).await.unwrap();
    assert!(second.replayed);

    let events = events_of(&ctx, first.transaction.id).await;
    assert_eq!(events.len(), 1, "el reenvío no genera un evento nuevo");
}

// ------------------------------------------------------------- 5

#[sqlx::test]
async fn el_relay_publica_marca_y_no_republica(pool: PgPool) {
    let ctx = from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let result = ctx
        .posting
        .post(&deposit(cash, customer, 60_000000, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    let spy = Arc::new(SpyPublisher::default());
    let relay = Relay::new(ctx.pool.clone(), Box::new(SharedPublisher(Arc::clone(&spy))));

    let published = relay.run_once().await.unwrap();
    assert!(published >= 1, "el relay publica lo pendiente");

    let ours: Vec<_> = spy
        .published()
        .into_iter()
        .filter(|m| m.aggregate_id == result.transaction.id)
        .collect();
    assert_eq!(ours.len(), 1);
    assert_eq!(ours[0].event_type, "aibank.events.v1.LedgerTransactionPosted");
    assert_eq!(ours[0].aggregate_type, "ledger_transaction");

    // Segunda pasada: lo ya publicado no vuelve a salir.
    relay.run_once().await.unwrap();
    let ours_after: Vec<_> = spy
        .published()
        .into_iter()
        .filter(|m| m.aggregate_id == result.transaction.id)
        .collect();
    assert_eq!(ours_after.len(), 1, "un evento marcado no se republica");
}

#[sqlx::test]
async fn el_relay_respeta_el_orden_de_emision(pool: PgPool) {
    let ctx = from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let spy = Arc::new(SpyPublisher::default());
    let relay = Relay::new(ctx.pool.clone(), Box::new(SharedPublisher(Arc::clone(&spy))));

    let mut expected = Vec::new();
    for amount in [10_000000, 20_000000, 30_000000] {
        let r = ctx
            .posting
            .post(&deposit(cash, customer, amount, &format!("dep-{}", Uuid::new_v4())))
            .await
            .unwrap();
        expected.push(r.transaction.id);
    }

    relay.run_once().await.unwrap();

    let got: Vec<_> = spy
        .published()
        .into_iter()
        .filter(|m| expected.contains(&m.aggregate_id))
        .map(|m| m.aggregate_id)
        .collect();
    assert_eq!(got, expected, "los eventos salen en el orden en que se asentaron");
}

// ------------------------------------------------------------- 6

#[sqlx::test]
async fn si_el_bus_falla_el_evento_queda_pendiente_y_se_reintenta(pool: PgPool) {
    let ctx = from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let spy = Arc::new(SpyPublisher::default());
    let relay = Relay::new(ctx.pool.clone(), Box::new(SharedPublisher(Arc::clone(&spy))));

    let result = ctx
        .posting
        .post(&deposit(cash, customer, 90_000000, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    spy.set_failing(true);
    let published = relay.run_once().await.unwrap();
    assert_eq!(published, 0, "con el bus caído no se marca nada como publicado");

    let (attempts, last_error) = sqlx::query_as::<_, (i32, Option<String>)>(
        "SELECT attempts, last_error FROM outbox WHERE aggregate_id = $1",
    )
    .bind(result.transaction.id)
    .fetch_one(&ctx.pool)
    .await
    .unwrap();
    assert!(attempts >= 1, "el intento fallido queda registrado");
    assert!(last_error.unwrap().contains("bus caído"));

    // El bus vuelve: el evento sigue ahí y se publica.
    spy.set_failing(false);
    relay.run_once().await.unwrap();

    let delivered = spy
        .published()
        .into_iter()
        .filter(|m| m.aggregate_id == result.transaction.id)
        .count();
    assert_eq!(delivered, 1, "ningún evento se pierde por una caída del bus");
}

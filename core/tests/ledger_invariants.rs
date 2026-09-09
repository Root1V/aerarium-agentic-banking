//! Invariantes del ledger, probadas contra PostgreSQL real.
//!
//! 1. Doble partida: solo se aceptan transacciones balanceadas (servicio Y base de datos).
//! 2. Append-only: UPDATE/DELETE sobre transacciones o asientos falla.
//! 3. Idempotencia: la misma clave = una sola transacción, incluso bajo concurrencia.
//! 4. La moneda del asiento coincide con la de la cuenta.
//! 5. Los saldos son proyección exacta de los asientos.
//!
//! Requiere Postgres: `docker compose -f platform/docker-compose.yml up -d`

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::{EntryCommand, PostingError, PostingRequest};
use common::*;
use std::sync::Arc;
use uuid::Uuid;

// ------------------------------------------------------------- 1. doble partida

#[tokio::test]
async fn posting_balanceado_actualiza_saldos_como_proyeccion_del_ledger() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting.post(&deposit(cash, customer, 150_000000, &format!("dep-{}", Uuid::new_v4())))
        .await.expect("first deposit");
    ctx.posting.post(&deposit(cash, customer, 50_000000, &format!("dep-{}", Uuid::new_v4())))
        .await.expect("second deposit");

    assert_eq!(ctx.accounts.balance_micros(cash).await.unwrap(), 200_000000, "activo crece al debe");
    assert_eq!(ctx.accounts.balance_micros(customer).await.unwrap(), 200_000000, "pasivo crece al haber");
}

#[tokio::test]
async fn posting_desbalanceado_es_rechazado_por_el_servicio() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let request = PostingRequest::new(
        format!("bad-{}", Uuid::new_v4()),
        "deposit",
        vec![
            EntryCommand::debit(cash, 100_000000, "USD"),
            EntryCommand::credit(customer, 99_000000, "USD"),
        ],
    );

    match ctx.posting.post(&request).await {
        Err(PostingError::Invalid(msg)) => assert!(msg.contains("unbalanced"), "mensaje: {msg}"),
        other => panic!("se esperaba Invalid, se obtuvo {other:?}"),
    }
}

#[tokio::test]
async fn la_base_rechaza_al_commit_un_insert_desbalanceado_que_salte_el_servicio() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, _customer) = ctx.cash_and_customer(&product).await;

    let mut tx = ctx.pool.begin().await.unwrap();
    sqlx::query(
        r#"
        WITH t AS (
            INSERT INTO ledger_transactions (idempotency_key, request_hash, kind)
            VALUES ($1, 'x', 'rogue') RETURNING id
        )
        INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_micros, currency)
        SELECT id, $2, 'DEBIT'::entry_direction, 100, 'USD' FROM t
        "#,
    )
    .bind(format!("rogue-{}", Uuid::new_v4()))
    .bind(cash)
    .execute(&mut *tx)
    .await
    .expect("el INSERT pasa; el trigger diferido debe abortar el COMMIT");

    let err = tx.commit().await.expect_err("el COMMIT debe fallar");
    let msg = err.to_string();
    assert!(
        msg.contains("unbalanced") || msg.contains("at least 2"),
        "mensaje inesperado: {msg}"
    );
}

#[tokio::test]
async fn un_monto_negativo_o_cero_es_rechazado() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let request = PostingRequest::new(
        format!("neg-{}", Uuid::new_v4()),
        "deposit",
        vec![
            EntryCommand::debit(cash, -5, "USD"),
            EntryCommand::credit(customer, -5, "USD"),
        ],
    );

    assert!(matches!(ctx.posting.post(&request).await, Err(PostingError::Invalid(_))));
}

// ------------------------------------------------------------- 2. append-only

#[tokio::test]
async fn los_asientos_y_transacciones_no_admiten_update_ni_delete() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let result = ctx.posting
        .post(&deposit(cash, customer, 10_000000, &format!("imm-{}", Uuid::new_v4())))
        .await
        .expect("deposit");

    let mutations = [
        "UPDATE ledger_entries SET amount_micros = 1 WHERE transaction_id = $1",
        "DELETE FROM ledger_entries WHERE transaction_id = $1",
        "UPDATE ledger_transactions SET kind = 'hacked' WHERE id = $1",
        "DELETE FROM ledger_transactions WHERE id = $1",
    ];

    for sql in mutations {
        let err = sqlx::query(sql)
            .bind(result.transaction.id)
            .execute(&ctx.pool)
            .await
            .expect_err(&format!("debería fallar: {sql}"));
        assert!(err.to_string().contains("append-only"), "mensaje inesperado: {err}");
    }
}

// ------------------------------------------------------------- 3. idempotencia

#[tokio::test]
async fn replay_con_la_misma_clave_devuelve_la_transaccion_original() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let key = format!("rep-{}", Uuid::new_v4());

    let first = ctx.posting.post(&deposit(cash, customer, 30_000000, &key)).await.unwrap();
    let second = ctx.posting.post(&deposit(cash, customer, 30_000000, &key)).await.unwrap();

    assert_eq!(first.transaction.id, second.transaction.id);
    assert!(second.replayed);
    assert_eq!(
        ctx.accounts.balance_micros(customer).await.unwrap(),
        30_000000,
        "el saldo refleja UN solo efecto"
    );
}

#[tokio::test]
async fn la_misma_clave_con_payload_distinto_es_un_conflicto_explicito() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let key = format!("conf-{}", Uuid::new_v4());

    ctx.posting.post(&deposit(cash, customer, 30_000000, &key)).await.unwrap();

    assert!(matches!(
        ctx.posting.post(&deposit(cash, customer, 99_990000, &key)).await,
        Err(PostingError::IdempotencyConflict(_))
    ));
}

#[tokio::test]
async fn bajo_concurrencia_n_intentos_con_la_misma_clave_producen_una_transaccion() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let key = format!("conc-{}", Uuid::new_v4());

    let ctx = Arc::new(ctx);
    let barrier = Arc::new(tokio::sync::Barrier::new(10));

    let mut handles = Vec::new();
    for _ in 0..10 {
        let ctx = Arc::clone(&ctx);
        let barrier = Arc::clone(&barrier);
        let key = key.clone();
        handles.push(tokio::spawn(async move {
            barrier.wait().await;
            ctx.posting.post(&deposit(cash, customer, 77_000000, &key)).await
        }));
    }

    let mut ids = Vec::new();
    let mut replayed = 0;
    for handle in handles {
        let result = handle.await.unwrap().expect("cada intento debe resolver, no fallar");
        if result.replayed {
            replayed += 1;
        }
        ids.push(result.transaction.id);
    }

    let unique: std::collections::HashSet<_> = ids.iter().collect();
    assert_eq!(unique.len(), 1, "todas las respuestas apuntan a la misma transacción");
    assert_eq!(replayed, 9, "solo una fue un insert real");
    assert_eq!(
        ctx.accounts.balance_micros(customer).await.unwrap(),
        77_000000,
        "efecto único pese a 10 intentos simultáneos"
    );

    let entry_count: i64 =
        sqlx::query_scalar("SELECT COUNT(*) FROM ledger_entries WHERE transaction_id = $1")
            .bind(ids[0])
            .fetch_one(&ctx.pool)
            .await
            .unwrap();
    assert_eq!(entry_count, 2);
}

// ------------------------------------------------------------- 4. monedas

#[tokio::test]
async fn un_asiento_en_moneda_distinta_a_la_de_la_cuenta_es_rechazado() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let request = PostingRequest::new(
        format!("fx-{}", Uuid::new_v4()),
        "deposit",
        vec![
            EntryCommand::debit(cash, 10_000000, "EUR"),
            EntryCommand::credit(customer, 10_000000, "EUR"),
        ],
    );

    match ctx.posting.post(&request).await {
        Err(PostingError::Database(e)) => assert!(
            e.to_string().contains("does not match account currency"),
            "mensaje inesperado: {e}"
        ),
        other => panic!("se esperaba error de base de datos, se obtuvo {other:?}"),
    }
}

// ------------------------------------------------------------- 5. proyección

#[tokio::test]
async fn una_transferencia_entre_clientes_mueve_saldos_y_la_caja_queda_intacta() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, alice) = ctx.cash_and_customer(&product).await;
    let bob_id = ctx.customer_account(&product).await;

    ctx.posting.post(&deposit(cash, alice, 100_000000, &format!("dep-{}", Uuid::new_v4())))
        .await.unwrap();

    ctx.posting
        .post(&PostingRequest::new(
            format!("xfer-{}", Uuid::new_v4()),
            "p2p_transfer",
            vec![
                EntryCommand::debit(alice, 40_000000, "USD"),
                EntryCommand::credit(bob_id, 40_000000, "USD"),
            ],
        ))
        .await
        .unwrap();

    assert_eq!(ctx.accounts.balance_micros(alice).await.unwrap(), 60_000000);
    assert_eq!(ctx.accounts.balance_micros(bob_id).await.unwrap(), 40_000000);
    assert_eq!(
        ctx.accounts.balance_micros(cash).await.unwrap(),
        100_000000,
        "la caja no cambia en un P2P interno"
    );
}

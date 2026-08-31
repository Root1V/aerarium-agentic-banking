//! Conciliación: el ledger contra sí mismo y contra los proveedores.
//!
//! El test más importante es el de deriva de saldo. Los triggers del esquema la
//! hacen imposible por la vía normal, así que aquí se provoca a mano — porque la
//! conciliación existe justamente para cazar lo que "no puede pasar": una
//! migración mal hecha, una corrección manual en producción, un bug futuro.

#![allow(clippy::inconsistent_digit_grouping)] // 100_00 = 100.00 en centavos

mod common;

use aibank_core::reconciliation::{ExternalMovement, FindingKind, Reconciler};
use aibank_core::AccountType;
use chrono::{Duration, Utc};
use common::*;
use sqlx::PgPool;
use uuid::Uuid;

// ---------------------------------------------------------------- interna

#[sqlx::test]
async fn un_ledger_sano_concilia_sin_diferencias(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting
        .post(&deposit(cash, customer, 300_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();
    ctx.posting
        .post(&withdrawal(cash, customer, 120_00, &format!("wd-{}", Uuid::new_v4())))
        .await
        .unwrap();

    let run = Reconciler::new(pool).check_internal().await.unwrap();

    assert!(run.is_clean(), "diferencias inesperadas: {:?}", run.findings);
}

#[sqlx::test]
async fn se_detecta_un_saldo_que_se_separo_de_sus_asientos(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting
        .post(&deposit(cash, customer, 200_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    // Se corrompe el saldo materializado a mano: es lo que haría una corrección
    // manual apresurada en producción o un bug en una migración futura.
    sqlx::query("UPDATE account_balances SET balance_minor = balance_minor + 50_00 WHERE account_id = $1")
        .bind(customer)
        .execute(&pool)
        .await
        .unwrap();

    let run = Reconciler::new(pool).check_internal().await.unwrap();

    assert_eq!(run.count_of(FindingKind::BalanceDrift), 1, "hallazgos: {:?}", run.findings);
    let finding = run.findings.first().unwrap();
    assert_eq!(finding.account_id, Some(customer));
    assert_eq!(finding.expected_minor, Some(200_00), "lo que dicen los asientos");
    assert_eq!(finding.actual_minor, Some(250_00), "lo que dice el saldo materializado");
}

// Un contador de movimientos alterado sin cambiar el importe también es deriva:
// dos errores que se compensan seguirían cuadrando por saldo.
#[sqlx::test]
async fn se_detecta_una_diferencia_en_el_numero_de_movimientos(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting
        .post(&deposit(cash, customer, 10_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();

    sqlx::query("UPDATE account_balances SET entry_count = entry_count + 3 WHERE account_id = $1")
        .bind(customer)
        .execute(&pool)
        .await
        .unwrap();

    let run = Reconciler::new(pool).check_internal().await.unwrap();
    assert_eq!(run.count_of(FindingKind::BalanceDrift), 1);
}

// ---------------------------------------------------------------- proveedor

/// Asienta un movimiento como lo haría un adaptador, con su clave de proveedor.
async fn post_from_provider(ctx: &Ctx, cash: Uuid, customer: Uuid, amount: i64, key: &str) {
    use aibank_core::{EntryCommand, PostingRequest};
    ctx.posting
        .post(&PostingRequest::new(
            key,
            "rail_inbound_credit",
            vec![
                EntryCommand::debit(cash, amount, "USD"),
                EntryCommand::credit(customer, amount, "USD"),
            ],
        ))
        .await
        .unwrap();
}

#[sqlx::test]
async fn un_extracto_que_coincide_no_genera_diferencias(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let since = Utc::now() - Duration::hours(1);

    let keys = ["rail:sim:in:tx-1", "rail:sim:in:tx-2"];
    post_from_provider(&ctx, cash, customer, 50_00, keys[0]).await;
    post_from_provider(&ctx, cash, customer, 75_00, keys[1]).await;

    let statement = vec![
        ExternalMovement {
            idempotency_key: keys[0].into(),
            amount_minor: 50_00,
            currency: "USD".into(),
            reference: "tx-1".into(),
        },
        ExternalMovement {
            idempotency_key: keys[1].into(),
            amount_minor: 75_00,
            currency: "USD".into(),
            reference: "tx-2".into(),
        },
    ];

    let run = Reconciler::new(pool)
        .check_against_provider("rail:sim", customer, &statement, since)
        .await
        .unwrap();

    assert!(run.is_clean(), "diferencias inesperadas: {:?}", run.findings);
}

// El caso que más duele: el proveedor movió dinero y nosotros no nos enteramos.
// Es un cliente sin acreditar.
#[sqlx::test]
async fn un_movimiento_del_proveedor_sin_asiento_se_reporta(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let since = Utc::now() - Duration::hours(1);

    post_from_provider(&ctx, cash, customer, 50_00, "rail:sim:in:tx-1").await;

    let statement = vec![
        ExternalMovement {
            idempotency_key: "rail:sim:in:tx-1".into(),
            amount_minor: 50_00,
            currency: "USD".into(),
            reference: "tx-1".into(),
        },
        // Este nunca llegó como webhook.
        ExternalMovement {
            idempotency_key: "rail:sim:in:tx-perdida".into(),
            amount_minor: 90_00,
            currency: "USD".into(),
            reference: "tx-perdida".into(),
        },
    ];

    let run = Reconciler::new(pool)
        .check_against_provider("rail:sim", customer, &statement, since)
        .await
        .unwrap();

    assert_eq!(run.count_of(FindingKind::MissingInLedger), 1);
    let finding = run
        .findings
        .iter()
        .find(|f| f.kind == FindingKind::MissingInLedger)
        .unwrap();
    assert_eq!(finding.reference.as_deref(), Some("tx-perdida"));
    assert_eq!(finding.expected_minor, Some(90_00));
}

// El caso inverso, igual de grave: acreditamos algo que el proveedor no respalda.
#[sqlx::test]
async fn un_asiento_que_el_proveedor_no_reporta_se_marca(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let since = Utc::now() - Duration::hours(1);

    post_from_provider(&ctx, cash, customer, 50_00, "rail:sim:in:tx-1").await;
    post_from_provider(&ctx, cash, customer, 30_00, "rail:sim:in:tx-fantasma").await;

    let statement = vec![ExternalMovement {
        idempotency_key: "rail:sim:in:tx-1".into(),
        amount_minor: 50_00,
        currency: "USD".into(),
        reference: "tx-1".into(),
    }];

    let run = Reconciler::new(pool)
        .check_against_provider("rail:sim", customer, &statement, since)
        .await
        .unwrap();

    assert_eq!(run.count_of(FindingKind::MissingAtProvider), 1);
    assert_eq!(
        run.findings[0].actual_minor,
        Some(30_00),
        "debe reportar el importe acreditado sin respaldo"
    );
}

#[sqlx::test]
async fn una_diferencia_de_importe_se_reporta_con_ambos_valores(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let since = Utc::now() - Duration::hours(1);

    post_from_provider(&ctx, cash, customer, 50_00, "rail:sim:in:tx-1").await;

    let statement = vec![ExternalMovement {
        idempotency_key: "rail:sim:in:tx-1".into(),
        amount_minor: 55_00, // el proveedor dice otra cosa
        currency: "USD".into(),
        reference: "tx-1".into(),
    }];

    let run = Reconciler::new(pool)
        .check_against_provider("rail:sim", customer, &statement, since)
        .await
        .unwrap();

    assert_eq!(run.count_of(FindingKind::AmountMismatch), 1);
    let finding = &run.findings[0];
    assert_eq!(finding.expected_minor, Some(55_00), "lo que dice el proveedor");
    assert_eq!(finding.actual_minor, Some(50_00), "lo que dice el ledger");
}

// Los movimientos de otros orígenes no ensucian la conciliación de este proveedor.
#[sqlx::test]
async fn la_conciliacion_se_limita_al_alcance_indicado(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let since = Utc::now() - Duration::hours(1);

    post_from_provider(&ctx, cash, customer, 50_00, "rail:sim:in:tx-1").await;
    // Un movimiento de tarjetas: no pertenece a este extracto.
    post_from_provider(&ctx, cash, customer, 20_00, "card:sim:clear:clr-1").await;

    let statement = vec![ExternalMovement {
        idempotency_key: "rail:sim:in:tx-1".into(),
        amount_minor: 50_00,
        currency: "USD".into(),
        reference: "tx-1".into(),
    }];

    let run = Reconciler::new(pool)
        .check_against_provider("rail:sim", customer, &statement, since)
        .await
        .unwrap();

    assert!(run.is_clean(), "el movimiento de tarjetas no debía entrar: {:?}", run.findings);
}

// ---------------------------------------------------------------- limbo

#[sqlx::test]
async fn el_dinero_detenido_en_transito_se_reporta(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let transit = ctx
        .accounts
        .create_internal(&format!("transit-{}", Uuid::new_v4()), "Tránsito", AccountType::Liability, "USD")
        .await
        .unwrap();

    ctx.posting
        .post(&deposit(cash, customer, 200_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();
    // Una salida que se quedó a medias: el dinero entró en tránsito y nunca salió.
    ctx.posting
        .post(&transfer(customer, transit.id, 80_00, &format!("out-{}", Uuid::new_v4())))
        .await
        .unwrap();

    // Con antigüedad negativa, todo lo asentado cuenta como vencido.
    let run = Reconciler::new(pool)
        .check_stale_suspense("rail:sim", transit.id, Duration::seconds(-1))
        .await
        .unwrap();

    assert_eq!(run.count_of(FindingKind::StaleSuspense), 1, "hallazgos: {:?}", run.findings);
    assert_eq!(run.findings[0].actual_minor, Some(80_00));
}

#[sqlx::test]
async fn una_cuenta_de_transito_saldada_no_genera_alerta(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let transit = ctx
        .accounts
        .create_internal(&format!("transit-{}", Uuid::new_v4()), "Tránsito", AccountType::Liability, "USD")
        .await
        .unwrap();

    ctx.posting
        .post(&deposit(cash, customer, 200_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();
    ctx.posting
        .post(&transfer(customer, transit.id, 80_00, &format!("out-{}", Uuid::new_v4())))
        .await
        .unwrap();
    // El movimiento se completó: el tránsito vuelve a cero.
    ctx.posting
        .post(&transfer(transit.id, cash, 80_00, &format!("settle-{}", Uuid::new_v4())))
        .await
        .unwrap();

    let run = Reconciler::new(pool)
        .check_stale_suspense("rail:sim", transit.id, Duration::seconds(-1))
        .await
        .unwrap();

    assert!(run.is_clean(), "el tránsito quedó saldado: {:?}", run.findings);
}

// ---------------------------------------------------------------- rastro

// Conciliar sin dejar constancia no sirve: hay que poder demostrar qué se detectó
// y cómo se resolvió.
#[sqlx::test]
async fn las_diferencias_quedan_abiertas_hasta_resolverse(pool: PgPool) {
    let ctx = from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting
        .post(&deposit(cash, customer, 100_00, &format!("dep-{}", Uuid::new_v4())))
        .await
        .unwrap();
    sqlx::query("UPDATE account_balances SET balance_minor = 1 WHERE account_id = $1")
        .bind(customer)
        .execute(&pool)
        .await
        .unwrap();

    let reconciler = Reconciler::new(pool);
    let run = reconciler.check_internal().await.unwrap();
    assert_eq!(run.findings.len(), 1);

    let open = reconciler.open_findings().await.unwrap();
    assert_eq!(open.len(), 1, "la diferencia debe quedar en la cola");

    let closed = reconciler
        .resolve(run.id, "ajuste contable aplicado por operaciones")
        .await
        .unwrap();
    assert_eq!(closed, 1);

    let open_after = reconciler.open_findings().await.unwrap();
    assert!(open_after.is_empty(), "resuelta, ya no debe estar abierta");
}

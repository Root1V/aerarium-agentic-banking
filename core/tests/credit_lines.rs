//! Líneas de crédito (tarjeta de crédito):
//!
//! 1. Abrir una línea crea una cuenta del cliente de tipo ACTIVO: lo que debe.
//! 2. Usarla (disposición) sube la deuda; nunca por encima del límite, tampoco
//!    bajo concurrencia.
//! 3. Pagarla baja la deuda; no se puede pagar más de lo que se debe.
//! 4. Bloqueada (mora) no admite usos, pero sí pagos.
//! 5. Una línea viva por cliente, con condiciones inmutables.

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::{AccountType, CreditLineStatus, CreditRepository, NewCreditLine, PostingError};
use common::*;
use std::sync::Arc;
use uuid::Uuid;

fn new_line(customer: Uuid, limit: i64) -> NewCreditLine {
    NewCreditLine {
        customer_id: customer,
        name: "Ana Pérez".into(),
        currency: "USD".into(),
        limit_micros: limit,
        annual_rate_bps: 4500,
        cycle_day: 5,
        payment_days: 20,
        decision_ref: format!("decision-{}", Uuid::new_v4()),
    }
}

/// Una línea de 500 y la cuenta de ahorro del mismo cliente, con 1.000 propios:
/// así un pago de más falla por la deuda y no por falta de fondos.
async fn line_and_deposit(ctx: &Ctx) -> (CreditRepository, aibank_core::CreditLine, Uuid) {
    let credit = CreditRepository::new(ctx.pool.clone());
    let line = credit
        .open(&new_line(Uuid::new_v4(), 500_000000))
        .await
        .expect("abrir línea");
    let product = ctx.simple_product(None, None).await;
    let (cash, deposit) = ctx.cash_and_customer(&product).await;
    ctx.posting
        .post(&deposit_to(cash, deposit, 1_000_000000))
        .await
        .expect("fondear la cuenta de ahorro");
    (credit, line, deposit)
}

fn deposit_to(cash: Uuid, account: Uuid, amount: i64) -> aibank_core::PostingRequest {
    deposit(cash, account, amount, &format!("dep-{}", Uuid::new_v4()))
}

/// Disposición: la deuda sube (debe en la línea) y el dinero llega a la cuenta.
fn draw(line: Uuid, deposit: Uuid, amount: i64) -> aibank_core::PostingRequest {
    let mut req = transfer(line, deposit, amount, &format!("draw-{}", Uuid::new_v4()));
    req.kind = "credit_drawdown".into();
    req
}

/// Pago: sale de la cuenta y la deuda baja.
fn pay(deposit: Uuid, line: Uuid, amount: i64) -> aibank_core::PostingRequest {
    let mut req = transfer(deposit, line, amount, &format!("pay-{}", Uuid::new_v4()));
    req.kind = "credit_payment".into();
    req
}

#[tokio::test]
async fn abrir_una_linea_crea_una_cuenta_de_activo_sin_deuda() {
    let ctx = setup().await;
    let (_, line, _) = line_and_deposit(&ctx).await;

    let account = ctx
        .accounts
        .find_by_id(line.account_id)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(
        account.account_type,
        AccountType::Asset,
        "lo que el cliente debe es activo del banco"
    );
    assert_eq!(account.owner_id, Some(line.customer_id));
    assert_eq!(line.status, CreditLineStatus::Active);
    assert_eq!(line.outstanding_micros, 0);
    assert_eq!(line.available_micros(), 500_000000);
}

#[tokio::test]
async fn usar_la_linea_sube_la_deuda_hasta_el_limite_y_no_mas() {
    let ctx = setup().await;
    let (credit, line, deposit) = line_and_deposit(&ctx).await;

    ctx.posting
        .post(&draw(line.account_id, deposit, 300_000000))
        .await
        .expect("disposición");
    let after = credit.find(line.id).await.unwrap().unwrap();
    assert_eq!(after.outstanding_micros, 300_000000);
    assert_eq!(after.available_micros(), 200_000000);
    assert_eq!(
        ctx.accounts.balance_micros(deposit).await.unwrap(),
        1_300_000000,
        "el dinero llegó a la cuenta"
    );

    let err = ctx
        .posting
        .post(&draw(line.account_id, deposit, 200_000001))
        .await
        .unwrap_err();
    assert!(
        matches!(err, PostingError::CreditLimitExceeded(_)),
        "un micro por encima: {err:?}"
    );

    ctx.posting
        .post(&draw(line.account_id, deposit, 200_000000))
        .await
        .expect("justo el límite sí");
}

#[tokio::test]
async fn disposiciones_simultaneas_no_superan_el_limite() {
    let ctx = Arc::new(setup().await);
    let (credit, line, deposit) = line_and_deposit(&ctx).await;

    // 10 disposiciones de 100 a la vez sobre una línea de 500: entran 5.
    let barrier = Arc::new(tokio::sync::Barrier::new(10));
    let mut handles = Vec::new();
    for _ in 0..10 {
        let ctx = Arc::clone(&ctx);
        let barrier = Arc::clone(&barrier);
        let account = line.account_id;
        handles.push(tokio::spawn(async move {
            barrier.wait().await;
            ctx.posting.post(&draw(account, deposit, 100_000000)).await
        }));
    }
    let mut ok = 0;
    for handle in handles {
        match handle.await.unwrap() {
            Ok(_) => ok += 1,
            Err(PostingError::CreditLimitExceeded(_)) => {}
            Err(other) => panic!("error inesperado: {other:?}"),
        }
    }
    assert_eq!(ok, 5);
    assert_eq!(
        credit
            .find(line.id)
            .await
            .unwrap()
            .unwrap()
            .outstanding_micros,
        500_000000
    );
}

#[tokio::test]
async fn pagar_baja_la_deuda_y_no_se_puede_pagar_de_mas() {
    let ctx = setup().await;
    let (credit, line, deposit) = line_and_deposit(&ctx).await;
    ctx.posting
        .post(&draw(line.account_id, deposit, 120_000000))
        .await
        .unwrap();

    ctx.posting
        .post(&pay(deposit, line.account_id, 20_000000))
        .await
        .expect("pago parcial");
    assert_eq!(
        credit
            .find(line.id)
            .await
            .unwrap()
            .unwrap()
            .outstanding_micros,
        100_000000
    );

    let err = ctx
        .posting
        .post(&pay(deposit, line.account_id, 100_000001))
        .await
        .unwrap_err();
    assert!(
        matches!(err, PostingError::Overpayment(_)),
        "pagar de más: {err:?}"
    );

    ctx.posting
        .post(&pay(deposit, line.account_id, 100_000000))
        .await
        .expect("pago total");
    assert_eq!(
        credit
            .find(line.id)
            .await
            .unwrap()
            .unwrap()
            .outstanding_micros,
        0
    );
}

#[tokio::test]
async fn una_linea_bloqueada_no_se_usa_pero_se_paga() {
    let ctx = setup().await;
    let (credit, line, deposit) = line_and_deposit(&ctx).await;
    ctx.posting
        .post(&draw(line.account_id, deposit, 100_000000))
        .await
        .unwrap();

    sqlx::query("UPDATE credit_lines SET status = 'BLOCKED' WHERE id = $1")
        .bind(line.id)
        .execute(&ctx.pool)
        .await
        .unwrap();

    let err = ctx
        .posting
        .post(&draw(line.account_id, deposit, 10_000000))
        .await
        .unwrap_err();
    assert!(
        matches!(err, PostingError::CreditLineInactive(_)),
        "usar bloqueada: {err:?}"
    );

    ctx.posting
        .post(&pay(deposit, line.account_id, 40_000000))
        .await
        .expect("pagar bloqueada sí");
    assert_eq!(
        credit
            .find(line.id)
            .await
            .unwrap()
            .unwrap()
            .outstanding_micros,
        60_000000
    );
}

#[tokio::test]
async fn una_linea_viva_por_cliente_con_condiciones_inmutables() {
    let ctx = setup().await;
    let credit = CreditRepository::new(ctx.pool.clone());
    let customer = Uuid::new_v4();
    let line = credit.open(&new_line(customer, 500_000000)).await.unwrap();

    let err = credit
        .open(&new_line(customer, 900_000000))
        .await
        .unwrap_err();
    assert!(
        matches!(err, PostingError::Conflict(_)),
        "segunda línea: {err:?}"
    );
    assert_eq!(
        credit
            .find_open_for_customer(customer)
            .await
            .unwrap()
            .unwrap()
            .id,
        line.id
    );

    // El límite se puede ajustar; la tasa del contrato, no.
    sqlx::query("UPDATE credit_lines SET limit_micros = 800000000 WHERE id = $1")
        .bind(line.id)
        .execute(&ctx.pool)
        .await
        .expect("subir el límite");
    let changed_rate = sqlx::query("UPDATE credit_lines SET annual_rate_bps = 9900 WHERE id = $1")
        .bind(line.id)
        .execute(&ctx.pool)
        .await;
    assert!(changed_rate.is_err(), "la tasa del contrato no cambia");
}

#[tokio::test]
async fn una_linea_sin_decision_o_con_condiciones_absurdas_no_se_abre() {
    let ctx = setup().await;
    let credit = CreditRepository::new(ctx.pool.clone());
    let base = new_line(Uuid::new_v4(), 500_000000);
    for bad in [
        NewCreditLine {
            limit_micros: 0,
            ..base.clone()
        },
        NewCreditLine {
            annual_rate_bps: -1,
            ..base.clone()
        },
        NewCreditLine {
            cycle_day: 31,
            ..base.clone()
        },
        NewCreditLine {
            decision_ref: " ".into(),
            ..base.clone()
        },
    ] {
        assert!(
            matches!(credit.open(&bad).await, Err(PostingError::Invalid(_))),
            "{bad:?}"
        );
    }
}

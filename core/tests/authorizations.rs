//! Autorizaciones: retener ahora, mover el dinero después.
//!
//! Lo que se prueba aquí no es la ruta feliz —esa es la fácil— sino las formas en
//! que un pago en dos tiempos puede perder o duplicar dinero: capturar dos veces,
//! capturar algo vencido, reembolsar lo que nunca se cobró, liberar lo ya
//! capturado, y dos operaciones concurrentes sobre la misma retención.

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::authorizations::{
    AuthorizationError, AuthorizationService, AuthorizationStatus, AuthorizeCommand, ObservedStatus,
};
use chrono::Utc;
use sqlx::PgPool;
use uuid::Uuid;

/// Escenario base: pagador con saldo, receptor, y la cuenta de retención.
struct Scene {
    auth: AuthorizationService,
    ctx: common::Ctx,
    payer: Uuid,
    payee: Uuid,
    holds: Uuid,
}

async fn scene(pool: PgPool, funded: i64) -> Scene {
    let ctx = common::from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let (cash, payer) = ctx.cash_and_customer(&product).await;
    let payee = ctx.customer_account(&product).await;
    let holds = ctx.holds_account("USD").await;

    if funded > 0 {
        ctx.posting
            .post(&common::deposit(cash, payer, funded, &format!("fund-{}", Uuid::new_v4())))
            .await
            .expect("fondear al pagador");
    }

    Scene { auth: AuthorizationService::new(pool), ctx, payer, payee, holds }
}

fn command(payer: Uuid, payee: Uuid, amount: i64) -> AuthorizeCommand {
    AuthorizeCommand {
        idempotency_key: format!("cart-{}", Uuid::new_v4()),
        payer_account_id: payer,
        payee_account_id: payee,
        amount_micros: amount,
        currency: "USD".into(),
        ttl_minutes: None,
    }
}

// ---------------------------------------------------------------- retener

#[sqlx::test]
async fn autorizar_retiene_el_dinero_sin_entregarlo(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let result = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    assert_eq!(result.authorization.status, AuthorizationStatus::Authorized);
    assert!(!result.replayed);

    // El dinero salió del pagador y está en retención, NO en el receptor: el
    // vendedor todavía no cobró nada.
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 70_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 30_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 0);
}

#[sqlx::test]
async fn una_autorizacion_nace_con_vencimiento(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let result = s.auth.authorize(&command(s.payer, s.payee, 10_000000)).await.unwrap();

    // Sin vencimiento, una autorización que nadie captura inmoviliza el dinero
    // del pagador para siempre.
    let ttl = result.authorization.expires_at - result.authorization.created_at;
    assert!(ttl.num_minutes() >= 14 && ttl.num_minutes() <= 15, "ttl = {ttl}");
    assert_eq!(result.authorization.observed(), ObservedStatus::Authorized);
}

#[sqlx::test]
async fn sin_saldo_no_hay_retencion_ni_autorizacion(pool: PgPool) {
    let s = scene(pool, 10_000000).await;

    let err = s.auth.authorize(&command(s.payer, s.payee, 50_000000)).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::InsufficientFunds(_)), "fue: {err:?}");

    // Lo importante no es el error sino que no quede rastro: el trigger de saldo
    // corre en el COMMIT, así que la fila de autorización tiene que morir con él.
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 10_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 0);

    let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM authorizations")
        .fetch_one(&s.ctx.pool)
        .await
        .unwrap();
    assert_eq!(count, 0, "una autorización sobrevivió a un COMMIT abortado");
}

#[sqlx::test]
async fn la_misma_clave_devuelve_la_autorizacion_original(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let cmd = command(s.payer, s.payee, 25_000000);

    let first = s.auth.authorize(&cmd).await.unwrap();
    let second = s.auth.authorize(&cmd).await.unwrap();

    assert_eq!(first.authorization.id, second.authorization.id);
    assert!(second.replayed, "un reintento de red no puede crear una segunda retención");
    // Y sobre todo: se retuvo UNA sola vez.
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 25_000000);
}

#[sqlx::test]
async fn la_misma_clave_con_otro_monto_es_un_error_explicito(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mut cmd = command(s.payer, s.payee, 25_000000);
    s.auth.authorize(&cmd).await.unwrap();

    cmd.amount_micros = 90_000000;
    let err = s.auth.authorize(&cmd).await.unwrap_err();

    // Cobrar 90 devolviendo la autorización de 25 sería un pago silenciosamente
    // distinto al que se pidió.
    assert!(matches!(err, AuthorizationError::IdempotencyConflict(_)), "fue: {err:?}");
}

#[sqlx::test]
async fn pagador_y_receptor_no_pueden_ser_la_misma_cuenta(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let err = s.auth.authorize(&command(s.payer, s.payer, 10_000000)).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::Invalid(_)), "fue: {err:?}");
}

#[sqlx::test]
async fn una_cuenta_inexistente_se_distingue_de_un_fallo(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let err = s.auth.authorize(&command(Uuid::new_v4(), s.payee, 10_000000)).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::AccountNotFound(_)), "fue: {err:?}");
}

#[sqlx::test]
async fn una_cuenta_congelada_no_puede_pagar(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    sqlx::query("UPDATE accounts SET status = 'FROZEN' WHERE id = $1")
        .bind(s.payer)
        .execute(&s.ctx.pool)
        .await
        .unwrap();

    let err = s.auth.authorize(&command(s.payer, s.payee, 10_000000)).await.unwrap_err();

    // Congelar una cuenta es una medida de control: tiene que impedir el pago
    // antes de retener, no fallar a mitad de camino.
    assert!(matches!(err, AuthorizationError::AccountNotOperative(_, _)), "fue: {err:?}");
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 0);
}

// ---------------------------------------------------------------- capturar

#[sqlx::test]
async fn capturar_mueve_el_dinero_de_la_retencion_al_receptor(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    let captured = s.auth.capture(auth.authorization.id).await.unwrap();

    assert_eq!(captured.status, AuthorizationStatus::Captured);
    assert!(captured.settled_at.is_some());
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 70_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 0);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 30_000000);
}

#[sqlx::test]
async fn capturar_dos_veces_devuelve_la_misma_captura(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    let first = s.auth.capture(auth.authorization.id).await.unwrap();
    let second = s.auth.capture(auth.authorization.id).await.unwrap();

    // Un timeout de red no se distingue de un fallo real desde el cliente.
    // Responder "ya capturada" a un reintento legítimo lo obligaría a adivinar si
    // el dinero se movió; devolver la misma captura no deja nada que adivinar.
    assert_eq!(first.settled_at, second.settled_at);
    assert_eq!(first.settle_transaction_id, second.settle_transaction_id);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 30_000000);
}

#[sqlx::test]
async fn una_autorizacion_vencida_no_se_captura(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    sqlx::query("UPDATE authorizations SET expires_at = now() - interval '1 minute' WHERE id = $1")
        .bind(auth.authorization.id)
        .execute(&s.ctx.pool)
        .await
        .unwrap();

    let err = s.auth.capture(auth.authorization.id).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::Expired(_, _)), "fue: {err:?}");
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 0);
}

#[sqlx::test]
async fn una_retencion_vencida_se_lee_como_vencida_aunque_el_dinero_siga_retenido(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    sqlx::query("UPDATE authorizations SET expires_at = now() - interval '1 minute' WHERE id = $1")
        .bind(auth.authorization.id)
        .execute(&s.ctx.pool)
        .await
        .unwrap();

    let found = s.auth.find(auth.authorization.id).await.unwrap().unwrap();

    // El barrendero todavía no pasó, así que en el ledger el dinero sigue en
    // retención. Aun así el estado reportado es "vencida": nunca se promete algo
    // que la contabilidad no vaya a cumplir.
    assert_eq!(found.status, AuthorizationStatus::Authorized);
    assert_eq!(found.observed(), ObservedStatus::Expired);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 30_000000);
}

#[sqlx::test]
async fn capturar_algo_inexistente_es_un_error_distinto(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let err = s.auth.capture(Uuid::new_v4()).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::NotFound(_)), "fue: {err:?}");
}

// ---------------------------------------------------------------- liberar

#[sqlx::test]
async fn liberar_devuelve_el_dinero_al_pagador(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    let released = s.auth.release(auth.authorization.id).await.unwrap();

    assert_eq!(released.status, AuthorizationStatus::Released);
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 100_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 0);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 0);
}

#[sqlx::test]
async fn el_barrendero_libera_las_vencidas_y_deja_las_vivas(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let vencida = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    let viva = s.auth.authorize(&command(s.payer, s.payee, 20_000000)).await.unwrap();

    sqlx::query("UPDATE authorizations SET expires_at = now() - interval '1 minute' WHERE id = $1")
        .bind(vencida.authorization.id)
        .execute(&s.ctx.pool)
        .await
        .unwrap();

    let released = s.auth.release_expired(100).await.unwrap();

    assert_eq!(released, vec![vencida.authorization.id]);
    // Vuelven los 30 de la vencida; los 20 de la viva siguen retenidos.
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 80_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 20_000000);
    assert_eq!(
        s.auth.find(viva.authorization.id).await.unwrap().unwrap().status,
        AuthorizationStatus::Authorized
    );
}

#[sqlx::test]
async fn no_se_puede_liberar_lo_ya_capturado(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    s.auth.capture(auth.authorization.id).await.unwrap();

    let err = s.auth.release(auth.authorization.id).await.unwrap_err();

    // Liberar después de capturar devolvería al pagador un dinero que el receptor
    // ya tiene: el banco pagaría dos veces el mismo cobro.
    assert!(matches!(err, AuthorizationError::InvalidState(_, _, _)), "fue: {err:?}");
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 30_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 70_000000);
}

#[sqlx::test]
async fn no_se_puede_capturar_lo_ya_liberado(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    s.auth.release(auth.authorization.id).await.unwrap();

    let err = s.auth.capture(auth.authorization.id).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::InvalidState(_, _, _)), "fue: {err:?}");
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 0);
}

// ---------------------------------------------------------------- reembolsar

#[sqlx::test]
async fn reembolsar_devuelve_el_dinero_capturado(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    s.auth.capture(auth.authorization.id).await.unwrap();

    let refunded = s.auth.refund(auth.authorization.id, None).await.unwrap();

    assert_eq!(refunded.status, AuthorizationStatus::Refunded);
    assert_eq!(refunded.refunded_micros, 30_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 100_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 0);
}

#[sqlx::test]
async fn el_reembolso_parcial_ya_funciona_en_el_modelo(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    s.auth.capture(auth.authorization.id).await.unwrap();

    let first = s.auth.refund(auth.authorization.id, Some(10_000000)).await.unwrap();
    assert_eq!(first.status, AuthorizationStatus::PartiallyRefunded);
    assert_eq!(first.refundable_micros(), 20_000000);

    let second = s.auth.refund(auth.authorization.id, Some(20_000000)).await.unwrap();
    assert_eq!(second.status, AuthorizationStatus::Refunded);

    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 100_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 0);
}

#[sqlx::test]
async fn no_se_reembolsa_mas_de_lo_cobrado(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    s.auth.capture(auth.authorization.id).await.unwrap();
    s.auth.refund(auth.authorization.id, Some(25_000000)).await.unwrap();

    let err = s.auth.refund(auth.authorization.id, Some(10_000000)).await.unwrap_err();
    assert!(matches!(err, AuthorizationError::Invalid(_)), "fue: {err:?}");

    // Solo volvieron los 25 devueltos, no 35.
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 95_000000);
}

#[sqlx::test]
async fn no_se_reembolsa_lo_que_nunca_se_cobro(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();

    let err = s.auth.refund(auth.authorization.id, None).await.unwrap_err();

    // Reembolsar una retención le daría al pagador un dinero que nunca perdió,
    // y dejaría los 30 retenidos sin dueño.
    assert!(matches!(err, AuthorizationError::InvalidState(_, _, _)), "fue: {err:?}");
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 70_000000);
}

#[sqlx::test]
async fn reembolsar_dos_veces_el_total_no_devuelve_el_doble(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    s.auth.capture(auth.authorization.id).await.unwrap();

    s.auth.refund(auth.authorization.id, None).await.unwrap();
    let second = s.auth.refund(auth.authorization.id, None).await.unwrap();

    assert_eq!(second.refunded_micros, 30_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.payer).await.unwrap(), 100_000000);
}

// ---------------------------------------------------------------- concurrencia

#[sqlx::test]
async fn dos_capturas_simultaneas_mueven_el_dinero_una_sola_vez(pool: PgPool) {
    let s = scene(pool.clone(), 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    let id = auth.authorization.id;

    // Es el caso que la retención existe para resistir: dos procesos capturando
    // la misma autorización a la vez. Sin el lock de fila, ambos leerían
    // AUTHORIZED y asentarían su movimiento antes de que el trigger rechace al
    // segundo.
    let a = AuthorizationService::new(pool.clone());
    let b = AuthorizationService::new(pool.clone());
    let (first, second) = tokio::join!(a.capture(id), b.capture(id));

    assert!(first.is_ok() && second.is_ok(), "una capturó y la otra falló: {first:?} {second:?}");
    assert_eq!(first.unwrap().settle_transaction_id, second.unwrap().settle_transaction_id);
    assert_eq!(s.ctx.accounts.balance_micros(s.payee).await.unwrap(), 30_000000);
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 0);
}

#[sqlx::test]
async fn capturar_y_liberar_a_la_vez_no_paga_dos_veces(pool: PgPool) {
    let s = scene(pool.clone(), 100_000000).await;
    let auth = s.auth.authorize(&command(s.payer, s.payee, 30_000000)).await.unwrap();
    let id = auth.authorization.id;

    let a = AuthorizationService::new(pool.clone());
    let b = AuthorizationService::new(pool.clone());
    let (capture, release) = tokio::join!(a.capture(id), b.release(id));

    // Exactamente una de las dos gana; la otra tiene que rebotar contra el estado.
    assert!(
        capture.is_ok() ^ release.is_ok(),
        "las dos prosperaron: capture={capture:?} release={release:?}"
    );

    // Pase lo que pase, la retención se vació una sola vez y el total se conserva.
    assert_eq!(s.ctx.accounts.balance_micros(s.holds).await.unwrap(), 0);
    let payer = s.ctx.accounts.balance_micros(s.payer).await.unwrap();
    let payee = s.ctx.accounts.balance_micros(s.payee).await.unwrap();
    assert_eq!(payer + payee, 100_000000, "apareció o desapareció dinero");
}

// ---------------------------------------------------------------- estado observable

#[test]
fn el_vencimiento_solo_afecta_a_una_retencion_viva() {
    use aibank_core::authorizations::Authorization;

    let base = Authorization {
        id: Uuid::new_v4(),
        idempotency_key: "k".into(),
        payer_account_id: Uuid::new_v4(),
        payee_account_id: Uuid::new_v4(),
        amount_micros: 1_000000,
        currency: "USD".into(),
        status: AuthorizationStatus::Authorized,
        refunded_micros: 0,
        hold_transaction_id: Uuid::new_v4(),
        settle_transaction_id: None,
        expires_at: Utc::now() - chrono::Duration::minutes(1),
        created_at: Utc::now(),
        settled_at: None,
        released_at: None,
    };

    assert_eq!(base.observed(), ObservedStatus::Expired);

    // Una captura anterior al vencimiento no se convierte en vencida porque pase
    // el tiempo: el dinero ya se movió.
    let captured = Authorization { status: AuthorizationStatus::Captured, ..base.clone() };
    assert_eq!(captured.observed(), ObservedStatus::Captured);

    let refunded = Authorization { status: AuthorizationStatus::Refunded, ..base };
    assert_eq!(refunded.observed(), ObservedStatus::Refunded);
}

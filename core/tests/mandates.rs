//! Mandatos de pago: el permiso que un titular le da a una plataforma.
//!
//! Lo que se prueba aquí es lo que un mandato tiene que IMPEDIR: gastar por
//! encima de lo autorizado, seguir gastando después de una revocación, gastar
//! desde una cuenta que el mandato no cubre, y que dos pagos simultáneos se
//! cuelen juntos por un tope que solo alcanzaba para uno.

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::AccountType;
use aibank_core::authorizations::{AuthorizationError, AuthorizationService, AuthorizeCommand};
use aibank_core::mandates::{
    GrantCommand, MandateError, MandateService, MandateStatus, ObservedMandateStatus,
};
use chrono::{Duration, Utc};
use sqlx::PgPool;
use uuid::Uuid;

struct Scene {
    mandates: MandateService,
    auth: AuthorizationService,
    ctx: common::Ctx,
    /// Cuenta de propósito del agente, bajo el titular.
    agent_account: Uuid,
    /// El titular humano que otorga el permiso.
    holder: Uuid,
    payee: Uuid,
    holds: Uuid,
}

async fn scene(pool: PgPool, funded: i64) -> Scene {
    let ctx = common::from_pool(pool.clone());
    let product = ctx.simple_product(None, None).await;
    let holds = ctx.holds_account("USD").await;
    let suffix = Uuid::new_v4();

    let cash = ctx
        .accounts
        .create_internal(&format!("cash-{suffix}"), "Caja", AccountType::Asset, "USD")
        .await
        .expect("caja");

    // La cuenta del agente cuelga del titular: mismo dueño, propósito acotado.
    let holder = Uuid::new_v4();
    let agent_account = ctx
        .accounts
        .open_customer_account(
            &format!("agent-{suffix}"),
            "Bolsillo del agente",
            holder,
            &product,
        )
        .await
        .expect("cuenta de agente")
        .id;

    let payee = ctx.customer_account(&product).await;

    if funded > 0 {
        ctx.posting
            .post(&common::deposit(
                cash.id,
                agent_account,
                funded,
                &format!("f-{suffix}"),
            ))
            .await
            .expect("fondear");
    }

    Scene {
        mandates: MandateService::new(pool.clone()),
        auth: AuthorizationService::new(pool),
        ctx,
        agent_account,
        holder,
        payee,
        holds,
    }
}

fn grant(account: Uuid, holder: Uuid, per_op: Option<i64>, total: Option<i64>) -> GrantCommand {
    GrantCommand {
        account_id: account,
        grantee: "mercatus_sandbox".into(),
        granted_by: holder,
        consent_reference: format!("consent-{}", Uuid::new_v4()),
        max_per_operation_micros: per_op,
        max_total_micros: total,
        expires_at: Utc::now() + Duration::days(30),
    }
}

fn payment(payer: Uuid, payee: Uuid, amount: i64) -> AuthorizeCommand {
    AuthorizeCommand {
        idempotency_key: format!("cart-{}", Uuid::new_v4()),
        payer_account_id: payer,
        payee_account_id: payee,
        amount_micros: amount,
        currency: "USD".into(),
        ttl_minutes: None,
    }
}

// ---------------------------------------------------------------- otorgar

#[sqlx::test]
async fn el_titular_otorga_un_permiso_acotado(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let mandate = s
        .mandates
        .grant(&grant(
            s.agent_account,
            s.holder,
            Some(10_000000),
            Some(50_000000),
        ))
        .await
        .unwrap();

    assert_eq!(mandate.status, MandateStatus::Active);
    assert_eq!(mandate.observed(), ObservedMandateStatus::Active);
    assert_eq!(mandate.currency, "USD", "hereda la moneda de la cuenta");
    assert!(mandate.is_usable());
}

#[sqlx::test]
async fn nadie_puede_delegar_sobre_una_cuenta_ajena(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    // Otro titular intenta otorgar permiso sobre la cuenta del primero.
    let err = s
        .mandates
        .grant(&grant(s.agent_account, Uuid::new_v4(), None, None))
        .await
        .unwrap_err();

    // Sin esta barrera, un fallo del flujo de consentimiento podría regalar
    // acceso al dinero de otra persona.
    assert!(matches!(err, MandateError::Invalid(_)), "fue: {err:?}");
}

#[sqlx::test]
async fn un_mandato_sin_evidencia_de_consentimiento_se_rechaza(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let mut command = grant(s.agent_account, s.holder, None, None);
    command.consent_reference = "  ".into();

    // Un mandato sin evidencia no prueba nada, y probar es lo único que
    // justifica su existencia.
    let err = s.mandates.grant(&command).await.unwrap_err();
    assert!(matches!(err, MandateError::Invalid(_)), "fue: {err:?}");
}

#[sqlx::test]
async fn un_mandato_ya_vencido_no_se_otorga(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    let mut command = grant(s.agent_account, s.holder, None, None);
    command.expires_at = Utc::now() - Duration::minutes(1);

    let err = s.mandates.grant(&command).await.unwrap_err();
    assert!(matches!(err, MandateError::Invalid(_)), "fue: {err:?}");
}

// ---------------------------------------------------------------- pagar

#[sqlx::test]
async fn con_mandato_vivo_el_pago_procede(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(
            s.agent_account,
            s.holder,
            Some(10_000000),
            Some(50_000000),
        ))
        .await
        .unwrap();

    let (result, usage) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 8_000000))
        .await
        .unwrap();

    assert!(!result.replayed);
    assert_eq!(usage.consumed_micros, 8_000000);
    assert_eq!(usage.remaining_micros(), Some(42_000000));
    // El dinero salió de la cuenta del agente, no de la principal del titular.
    assert_eq!(
        s.ctx
            .accounts
            .balance_micros(s.agent_account)
            .await
            .unwrap(),
        92_000000
    );
    assert_eq!(
        s.ctx.accounts.balance_micros(s.holds).await.unwrap(),
        8_000000
    );
}

#[sqlx::test]
async fn un_pago_mayor_al_tope_por_operacion_se_rechaza(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, Some(10_000000), None))
        .await
        .unwrap();

    let err = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 25_000000))
        .await
        .unwrap_err();

    // Hay saldo de sobra: lo que falta es permiso. Son cosas distintas y el
    // cliente tiene que poder distinguirlas.
    assert!(
        matches!(err, MandateError::LimitExceeded(_)),
        "fue: {err:?}"
    );
    assert_eq!(
        s.ctx
            .accounts
            .balance_micros(s.agent_account)
            .await
            .unwrap(),
        100_000000
    );
}

// Al revés que el anterior: hay permiso de sobra y lo que falta es saldo. Llega en
// el COMMIT, por los triggers diferidos del ledger, y tiene que salir como fondos
// insuficientes, no como un error de base de datos (M-07).
#[sqlx::test]
async fn sin_saldo_el_pago_falla_por_fondos_insuficientes(pool: PgPool) {
    let s = scene(pool, 5_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let err = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 8_000000))
        .await
        .unwrap_err();

    assert!(
        matches!(
            err,
            MandateError::Authorization(AuthorizationError::InsufficientFunds(_))
        ),
        "fue: {err:?}"
    );
    assert_eq!(
        s.ctx
            .accounts
            .balance_micros(s.agent_account)
            .await
            .unwrap(),
        5_000000
    );
}

#[sqlx::test]
async fn el_tope_total_se_agota_con_el_uso(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, Some(30_000000)))
        .await
        .unwrap();

    s.mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 20_000000))
        .await
        .unwrap();

    let err = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 15_000000))
        .await
        .unwrap_err();
    assert!(
        matches!(err, MandateError::LimitExceeded(_)),
        "fue: {err:?}"
    );

    // Lo que sí cabe, pasa.
    let (_, usage) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 10_000000))
        .await
        .unwrap();
    assert_eq!(usage.remaining_micros(), Some(0));
}

#[sqlx::test]
async fn una_retencion_viva_ya_consume_el_tope(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, Some(30_000000)))
        .await
        .unwrap();

    // Autoriza sin capturar. El dinero está comprometido aunque no se haya
    // movido al receptor: contar solo lo capturado dejaría al agente autorizando
    // muchas veces por encima del tope mientras nada se captura.
    s.mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 25_000000))
        .await
        .unwrap();

    let err = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 10_000000))
        .await
        .unwrap_err();
    assert!(
        matches!(err, MandateError::LimitExceeded(_)),
        "fue: {err:?}"
    );
}

#[sqlx::test]
async fn liberar_una_retencion_devuelve_el_tope(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, Some(30_000000)))
        .await
        .unwrap();

    let (result, _) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 25_000000))
        .await
        .unwrap();

    s.auth.release(result.authorization.id).await.unwrap();

    // El dinero volvió, así que el tope también. Es lo que hace que derivar el
    // consumo sea mejor que llevar un contador: no hay nada que recordar bajar.
    let usage = s.mandates.find(mandate.id).await.unwrap().unwrap();
    assert_eq!(usage.consumed_micros, 0);
    assert_eq!(usage.remaining_micros(), Some(30_000000));

    s.mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 30_000000))
        .await
        .expect("el tope liberado tiene que volver a estar disponible");
}

#[sqlx::test]
async fn reembolsar_devuelve_el_tope(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, Some(30_000000)))
        .await
        .unwrap();

    let (result, _) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 20_000000))
        .await
        .unwrap();
    s.auth.capture(result.authorization.id).await.unwrap();
    s.auth.refund(result.authorization.id, None).await.unwrap();

    let usage = s.mandates.find(mandate.id).await.unwrap().unwrap();
    assert_eq!(
        usage.consumed_micros, 0,
        "un cobro devuelto no puede seguir contra el tope"
    );
}

#[sqlx::test]
async fn el_mandato_no_cubre_otra_cuenta(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let product = s.ctx.simple_product(None, None).await;
    let otra = s.ctx.customer_account(&product).await;

    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let err = s
        .mandates
        .authorize(mandate.id, &payment(otra, s.payee, 1_000000))
        .await
        .unwrap_err();

    // Un mandato autoriza UNA cuenta. Si cubriera cualquiera del titular, el
    // aislamiento de la cuenta de propósito no serviría de nada.
    assert!(
        matches!(err, MandateError::AccountNotCovered(_, _)),
        "fue: {err:?}"
    );
}

// ---------------------------------------------------------------- revocar

#[sqlx::test]
async fn tras_revocar_no_se_puede_iniciar_un_pago_nuevo(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let revoked = s.mandates.revoke(mandate.id, "titular").await.unwrap();
    assert_eq!(revoked.status, MandateStatus::Revoked);
    assert!(revoked.revoked_at.is_some());

    let err = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 1_000000))
        .await
        .unwrap_err();
    assert!(matches!(err, MandateError::Revoked(_)), "fue: {err:?}");
}

#[sqlx::test]
async fn revocar_no_cancela_una_retencion_viva(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let (result, _) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 5_000000))
        .await
        .unwrap();

    s.mandates.revoke(mandate.id, "titular").await.unwrap();

    // Del otro lado puede haber un vendedor que ya entregó lo que se le pagó.
    // Dejarlo sin cobro sería trasladarle un problema que no es suyo; la
    // exposición queda acotada por la vigencia de la retención.
    s.auth
        .capture(result.authorization.id)
        .await
        .expect("una retención anterior a la revocación tiene que poder cobrarse");
    assert_eq!(
        s.ctx.accounts.balance_micros(s.payee).await.unwrap(),
        5_000000
    );
}

#[sqlx::test]
async fn revocar_dos_veces_es_inofensivo(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let first = s.mandates.revoke(mandate.id, "titular").await.unwrap();
    let second = s.mandates.revoke(mandate.id, "operaciones").await.unwrap();

    assert_eq!(
        first.revoked_at, second.revoked_at,
        "la segunda no puede pisar la primera"
    );
    assert_eq!(second.revoked_by.as_deref(), Some("titular"));
}

#[sqlx::test]
async fn un_mandato_revocado_no_se_puede_reactivar(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();
    s.mandates.revoke(mandate.id, "titular").await.unwrap();

    // La barrera está en la base: resucitar un permiso que una persona retiró no
    // puede depender de que ninguna capa de arriba se equivoque.
    let err = sqlx::query("UPDATE payment_mandates SET status = 'ACTIVE' WHERE id = $1")
        .bind(mandate.id)
        .execute(&s.ctx.pool)
        .await
        .unwrap_err();
    assert!(err.to_string().contains("revoked"), "fue: {err}");
}

#[sqlx::test]
async fn los_topes_de_un_mandato_son_inmutables(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, Some(1_000000), None))
        .await
        .unwrap();

    // Es el registro que se presenta en una disputa: tiene que decir lo mismo
    // que la persona aprobó en pantalla.
    let err = sqlx::query(
        "UPDATE payment_mandates SET max_per_operation_micros = 999_000000 WHERE id = $1",
    )
    .bind(mandate.id)
    .execute(&s.ctx.pool)
    .await
    .unwrap_err();
    assert!(err.to_string().contains("immutable"), "fue: {err}");
}

// ---------------------------------------------------------------- vencimiento

#[sqlx::test]
async fn un_mandato_vencido_no_habilita_pagos(pool: PgPool) {
    let s = scene(pool, 100_000000).await;

    // Se inserta directamente porque `expires_at` es inmutable: no hay forma de
    // vencer un mandato existente, y esa es la regla que se quiere conservar —
    // extender un permiso exige revocarlo y otorgar otro que la persona apruebe.
    let id: Uuid = sqlx::query_scalar(
        r#"INSERT INTO payment_mandates
             (account_id, grantee, granted_by, consent_reference, currency, expires_at)
           VALUES ($1, 'mercatus_sandbox', $2, 'consent-vencido', 'USD', now() - interval '1 day')
           RETURNING id"#,
    )
    .bind(s.agent_account)
    .bind(s.holder)
    .fetch_one(&s.ctx.pool)
    .await
    .unwrap();
    let mandate = s.mandates.find(id).await.unwrap().unwrap().mandate;
    assert_eq!(mandate.observed(), ObservedMandateStatus::Expired);

    let err = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 1_000000))
        .await
        .unwrap_err();
    assert!(matches!(err, MandateError::Expired(_, _)), "fue: {err:?}");

    // Y no aparece como permiso vivo para la integración.
    let found = s
        .mandates
        .find_active_for(s.agent_account, "mercatus_sandbox")
        .await
        .unwrap();
    assert!(
        found.is_none(),
        "un mandato vencido no puede figurar como vivo"
    );
}

// ---------------------------------------------------------------- consulta

#[sqlx::test]
async fn la_integracion_encuentra_su_permiso_y_no_el_de_otra(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    s.mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    assert!(
        s.mandates
            .find_active_for(s.agent_account, "mercatus_sandbox")
            .await
            .unwrap()
            .is_some()
    );
    assert!(
        s.mandates
            .find_active_for(s.agent_account, "otra_plataforma")
            .await
            .unwrap()
            .is_none(),
        "un permiso otorgado a una plataforma no puede servirle a otra"
    );
}

#[sqlx::test]
async fn el_titular_ve_sus_mandatos(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    s.mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();
    s.mandates
        .grant(&grant(s.agent_account, s.holder, Some(5_000000), None))
        .await
        .unwrap();

    let mine = s.mandates.list_for_holder(s.holder).await.unwrap();
    assert_eq!(mine.len(), 2);

    // Sin esto, revocar sería imposible en la práctica: no se puede retirar un
    // permiso que no se puede ver.
    let ajenos = s.mandates.list_for_holder(Uuid::new_v4()).await.unwrap();
    assert!(ajenos.is_empty());
}

// ---------------------------------------------------------------- concurrencia

#[sqlx::test]
async fn dos_pagos_simultaneos_no_se_cuelan_juntos_por_el_tope(pool: PgPool) {
    let s = scene(pool.clone(), 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, Some(30_000000)))
        .await
        .unwrap();

    // El tope alcanza para uno de los dos, no para ambos.
    let a = MandateService::new(pool.clone());
    let b = MandateService::new(pool.clone());
    let uno = payment(s.agent_account, s.payee, 20_000000);
    let dos = payment(s.agent_account, s.payee, 20_000000);
    let (first, second) =
        tokio::join!(a.authorize(mandate.id, &uno), b.authorize(mandate.id, &dos),);

    assert!(
        first.is_ok() ^ second.is_ok(),
        "pasaron los dos o ninguno: {first:?} {second:?}"
    );

    // Solo se comprometió un pago.
    let usage = s.mandates.find(mandate.id).await.unwrap().unwrap();
    assert_eq!(usage.consumed_micros, 20_000000);
    assert_eq!(
        s.ctx.accounts.balance_micros(s.holds).await.unwrap(),
        20_000000
    );
}

#[sqlx::test]
async fn pagar_y_revocar_a_la_vez_no_deja_un_pago_sin_permiso(pool: PgPool) {
    let s = scene(pool.clone(), 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let a = MandateService::new(pool.clone());
    let b = MandateService::new(pool.clone());
    let cobro = payment(s.agent_account, s.payee, 5_000000);
    let (pago, _) = tokio::join!(
        a.authorize(mandate.id, &cobro),
        b.revoke(mandate.id, "titular"),
    );

    // Cualquiera de los dos órdenes es correcto; lo que no puede pasar es que el
    // pago prospere Y quede sin mandato que lo respalde.
    let comprometido = s.ctx.accounts.balance_micros(s.holds).await.unwrap();
    if pago.is_ok() {
        assert_eq!(comprometido, 5_000000);
        let usage = s.mandates.find(mandate.id).await.unwrap().unwrap();
        assert_eq!(
            usage.consumed_micros, 5_000000,
            "el pago quedó sin registrar en el mandato"
        );
    } else {
        assert_eq!(comprometido, 0, "se retuvo dinero de un pago que falló");
    }
}

// ---------------------------------------------------------------- lo retenido

// Lo que ve el titular en la app: cuánto tiene retenido y para quién.

#[sqlx::test]
async fn sin_pagos_en_curso_no_hay_nada_retenido(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let groups = s.auth.summarize_live_holds(s.agent_account).await.unwrap();
    assert!(groups.is_empty());
}

#[sqlx::test]
async fn lo_retenido_se_suma_en_el_servidor_aunque_sean_cientos(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    // 150 micropagos de $0,001: más de lo que cabría en una página de cien. Si
    // el total se calculara sumando una lista paginada, saldría mal.
    for _ in 0..150 {
        s.mandates
            .authorize(mandate.id, &payment(s.agent_account, s.payee, 1_000))
            .await
            .unwrap();
    }

    let groups = s.auth.summarize_live_holds(s.agent_account).await.unwrap();
    assert_eq!(groups.len(), 1);
    assert_eq!(groups[0].grantee.as_deref(), Some("mercatus_sandbox"));
    assert_eq!(groups[0].count, 150);
    assert_eq!(groups[0].total_micros, 150_000);
    assert_eq!(groups[0].expired_micros, 0);

    // Y cuadra con el ledger: lo retenido es exactamente lo que falta del saldo.
    assert_eq!(
        s.ctx
            .accounts
            .balance_micros(s.agent_account)
            .await
            .unwrap(),
        100_000000 - 150_000
    );
}

#[sqlx::test]
async fn lo_cobrado_y_lo_liberado_ya_no_esta_retenido(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let mut ids = Vec::new();
    for amount in [1_000000, 2_000000, 3_000000] {
        let (result, _) = s
            .mandates
            .authorize(mandate.id, &payment(s.agent_account, s.payee, amount))
            .await
            .unwrap();
        ids.push(result.authorization.id);
    }

    // El de 1 se cobra y el de 2 se libera: solo queda retenido el de 3.
    s.auth.capture(ids[0]).await.unwrap();
    s.auth.release(ids[1]).await.unwrap();

    let groups = s.auth.summarize_live_holds(s.agent_account).await.unwrap();
    assert_eq!(groups.len(), 1);
    assert_eq!(groups[0].count, 1);
    assert_eq!(groups[0].total_micros, 3_000000);
}

#[sqlx::test]
async fn lo_vencido_sigue_retenido_pero_se_distingue(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let (vencida, _) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 4_000000))
        .await
        .unwrap();
    s.mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 6_000000))
        .await
        .unwrap();

    // Vence, pero el barrendero todavía no pasó: el dinero sigue fuera del
    // saldo. Contarlo como "nada" mentiría sobre el saldo; contarlo como cobro
    // pendiente prometería algo que ya no puede ocurrir.
    sqlx::query("UPDATE authorizations SET expires_at = now() - interval '1 minute' WHERE id = $1")
        .bind(vencida.authorization.id)
        .execute(&s.ctx.pool)
        .await
        .unwrap();

    let groups = s.auth.summarize_live_holds(s.agent_account).await.unwrap();
    assert_eq!(groups[0].total_micros, 10_000000);
    assert_eq!(groups[0].expired_micros, 4_000000);
}

#[sqlx::test]
async fn se_agrupa_por_quien_cobra_y_lo_mayor_va_primero(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mercatus = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();
    let mut otra = grant(s.agent_account, s.holder, None, None);
    otra.grantee = "otra_plataforma".into();
    let otra = s.mandates.grant(&otra).await.unwrap();

    s.mandates
        .authorize(mercatus.id, &payment(s.agent_account, s.payee, 1_000000))
        .await
        .unwrap();
    s.mandates
        .authorize(otra.id, &payment(s.agent_account, s.payee, 5_000000))
        .await
        .unwrap();

    let groups = s.auth.summarize_live_holds(s.agent_account).await.unwrap();
    let resumen: Vec<_> = groups
        .iter()
        .map(|g| (g.grantee.as_deref(), g.total_micros))
        .collect();
    assert_eq!(
        resumen,
        vec![
            (Some("otra_plataforma"), 5_000000),
            (Some("mercatus_sandbox"), 1_000000)
        ]
    );
}

#[sqlx::test]
async fn una_retencion_sin_mandato_no_se_atribuye_a_nadie(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    s.auth
        .authorize(&payment(s.agent_account, s.payee, 2_000000))
        .await
        .unwrap();

    let groups = s.auth.summarize_live_holds(s.agent_account).await.unwrap();
    assert_eq!(groups.len(), 1);
    // Sin mandato no hay plataforma a la que atribuirla. Inventar una sería peor
    // que decir que no se sabe.
    assert_eq!(groups[0].grantee, None);
    assert_eq!(groups[0].total_micros, 2_000000);
}

// ---------------------------------------------------------------- centro de control

// Lo que ve el titular en el centro de control: cuánto gastó cada plataforma
// hoy y este mes, y a qué plataforma pertenece cada pago del historial.

async fn backdate(s: &Scene, authorization: Uuid, at: chrono::DateTime<Utc>) {
    sqlx::query("UPDATE authorizations SET created_at = $2 WHERE id = $1")
        .bind(authorization)
        .bind(at)
        .execute(&s.ctx.pool)
        .await
        .unwrap();
}

fn at(rfc3339: &str) -> chrono::DateTime<Utc> {
    chrono::DateTime::parse_from_rfc3339(rfc3339)
        .unwrap()
        .with_timezone(&Utc)
}

#[sqlx::test]
async fn el_gasto_se_separa_en_hoy_y_en_el_mes(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let pay = async |amount: i64, when: &str| {
        let (r, _) = s
            .mandates
            .authorize(mandate.id, &payment(s.agent_account, s.payee, amount))
            .await
            .unwrap();
        backdate(&s, r.authorization.id, at(when)).await;
        r.authorization.id
    };

    // "Ahora" fijo: 20 de octubre a las 10:00 en Lima (15:00 UTC).
    let now = at("2026-10-20T15:00:00Z");
    pay(1_000000, "2026-10-20T14:00:00Z").await; // hoy
    pay(2_000000, "2026-10-19T15:00:00Z").await; // ayer, mismo mes
    pay(4_000000, "2026-09-30T15:00:00Z").await; // mes anterior

    let spend = s
        .mandates
        .summarize_spend(s.holder, now, "America/Lima")
        .await
        .unwrap();
    let m = &spend[0];
    assert_eq!((m.today_micros, m.today_count), (1_000000, 1));
    assert_eq!((m.month_micros, m.month_count), (3_000000, 2));
}

#[sqlx::test]
async fn hoy_es_el_hoy_de_quien_mira_no_el_de_utc(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();
    let (r, _) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 1_000000))
        .await
        .unwrap();

    // El pago fue a las 15:00 en Lima. Son las 22:30 en Lima, que en UTC ya es
    // el día siguiente.
    backdate(&s, r.authorization.id, at("2026-10-20T20:00:00Z")).await;
    let now = at("2026-10-21T03:30:00Z");

    let lima = s
        .mandates
        .summarize_spend(s.holder, now, "America/Lima")
        .await
        .unwrap();
    let utc = s
        .mandates
        .summarize_spend(s.holder, now, "UTC")
        .await
        .unwrap();

    // Para quien vive en Lima, ese pago es de hoy. Calculado en UTC, a esta hora
    // la persona vería su gasto del día en cero.
    assert_eq!(lima[0].today_count, 1);
    assert_eq!(utc[0].today_count, 0);
}

#[sqlx::test]
async fn lo_reembolsado_se_descuenta_y_lo_liberado_no_cuenta(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let mut ids = Vec::new();
    for amount in [5_000000, 3_000000, 2_000000] {
        let (r, _) = s
            .mandates
            .authorize(mandate.id, &payment(s.agent_account, s.payee, amount))
            .await
            .unwrap();
        ids.push(r.authorization.id);
    }
    // 5 cobrado y reembolsado en 1; 3 liberado; 2 sigue retenido.
    s.auth.capture(ids[0]).await.unwrap();
    s.auth.refund(ids[0], Some(1_000000)).await.unwrap();
    s.auth.release(ids[1]).await.unwrap();

    let spend = s
        .mandates
        .summarize_spend(s.holder, Utc::now(), "America/Lima")
        .await
        .unwrap();
    let m = &spend[0];
    // La misma regla que el consumo del mandato: la cifra que ve la persona es
    // la que va agotando su tope.
    assert_eq!(m.today_micros, 4_000000 + 2_000000);
    assert_eq!(m.today_count, 2);
    assert_eq!(m.in_flight_micros, 2_000000);

    let usage = s.mandates.find(mandate.id).await.unwrap().unwrap();
    assert_eq!(m.month_micros, usage.consumed_micros);
}

#[sqlx::test]
async fn un_permiso_sin_pagos_aparece_en_cero(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let spend = s
        .mandates
        .summarize_spend(s.holder, Utc::now(), "America/Lima")
        .await
        .unwrap();
    // Que no aparezca haría creer que el permiso no existe.
    assert_eq!(spend.len(), 1);
    assert_eq!(spend[0].mandate_id, mandate.id);
    assert_eq!((spend[0].today_micros, spend[0].month_micros), (0, 0));
}

#[sqlx::test]
async fn una_zona_horaria_desconocida_es_un_error_del_llamador(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    s.mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let err = s
        .mandates
        .summarize_spend(s.holder, Utc::now(), "Lima/Peru")
        .await
        .unwrap_err();
    assert!(matches!(err, MandateError::Invalid(_)), "{err:?}");
}

#[sqlx::test]
async fn cada_retencion_bajo_mandato_dice_de_que_plataforma_es(pool: PgPool) {
    let s = scene(pool, 100_000000).await;
    let mandate = s
        .mandates
        .grant(&grant(s.agent_account, s.holder, None, None))
        .await
        .unwrap();

    let (con_mandato, _) = s
        .mandates
        .authorize(mandate.id, &payment(s.agent_account, s.payee, 1_000000))
        .await
        .unwrap();
    let sin_mandato = s
        .auth
        .authorize(&payment(s.agent_account, s.payee, 1_000000))
        .await
        .unwrap();

    let found = s
        .auth
        .lookup_hold_grantees(&[
            con_mandato.authorization.hold_transaction_id,
            sin_mandato.authorization.hold_transaction_id,
            Uuid::new_v4(),
        ])
        .await
        .unwrap();

    // Solo la retención hecha bajo un mandato tiene plataforma. Las demás no
    // aparecen: inventarles una sería peor que no decir nada.
    assert_eq!(
        found,
        vec![(
            con_mandato.authorization.hold_transaction_id,
            "mercatus_sandbox".to_string()
        )]
    );
}

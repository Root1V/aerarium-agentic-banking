//! Bolsillos: dinero apartado dentro de la cuenta.
//!
//! Lo que se prueba es lo que un bolsillo NO puede ser: una forma de guardar más
//! de lo que el producto permite, una puerta para mover dinero a otro con la
//! etiqueta de «bolsillo», o un sitio donde el dinero desaparece al cerrarlo.

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::{AccountStatus, EntryCommand, PostingError, PostingRequest};
use sqlx::PgPool;
use uuid::Uuid;

fn pocket_move(from: Uuid, to: Uuid, amount: i64) -> PostingRequest {
    PostingRequest::new(
        format!("pocket-{}", Uuid::new_v4()),
        "pocket_transfer",
        vec![
            EntryCommand::debit(from, amount, "USD"),
            EntryCommand::credit(to, amount, "USD"),
        ],
    )
}

#[sqlx::test]
async fn un_bolsillo_hereda_cliente_moneda_y_producto(pool: PgPool) {
    let ctx = common::from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (_, account) = ctx.cash_and_customer(&product).await;
    let parent = ctx.accounts.find_by_id(account).await.unwrap().unwrap();

    let pocket = ctx
        .accounts
        .open_pocket(
            account,
            &format!("pk-{}", Uuid::new_v4()),
            "Para la tarjeta",
        )
        .await
        .unwrap();

    assert_eq!(pocket.parent_account_id, Some(account));
    assert_eq!(pocket.owner_id, parent.owner_id);
    assert_eq!(pocket.product_id, parent.product_id);
    assert_eq!(pocket.currency, "USD");
    // Aparece entre las cuentas del cliente, marcado como bolsillo.
    let all = ctx
        .accounts
        .list_for_customer(parent.owner_id.unwrap())
        .await
        .unwrap();
    assert!(
        all.iter()
            .any(|a| a.id == pocket.id && a.parent_account_id == Some(account))
    );
}

#[sqlx::test]
async fn no_hay_bolsillos_de_bolsillos_ni_de_cuentas_internas(pool: PgPool) {
    let ctx = common::from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, account) = ctx.cash_and_customer(&product).await;
    let pocket = ctx
        .accounts
        .open_pocket(account, &format!("pk-{}", Uuid::new_v4()), "Vacaciones")
        .await
        .unwrap();

    let nested = ctx
        .accounts
        .open_pocket(pocket.id, &format!("pk-{}", Uuid::new_v4()), "Dentro")
        .await
        .unwrap_err();
    assert!(
        matches!(nested, PostingError::Invalid(_)),
        "fue: {nested:?}"
    );

    let internal = ctx
        .accounts
        .open_pocket(cash, &format!("pk-{}", Uuid::new_v4()), "De la caja")
        .await
        .unwrap_err();
    assert!(
        matches!(internal, PostingError::Invalid(_)),
        "fue: {internal:?}"
    );
}

// Mover a un bolsillo no es una operación con terceros: no cuenta contra el tope
// por operación. Pero el tope de saldo es de la cuenta con sus bolsillos.
#[sqlx::test]
async fn el_tope_de_saldo_es_de_la_cuenta_con_sus_bolsillos(pool: PgPool) {
    let ctx = common::from_pool(pool);
    let product = ctx
        .simple_product(Some(1_000_000000), Some(300_000000))
        .await;
    let (cash, account) = ctx.cash_and_customer(&product).await;
    let pocket = ctx
        .accounts
        .open_pocket(account, &format!("pk-{}", Uuid::new_v4()), "Ahorro")
        .await
        .unwrap();
    for i in 0..3 {
        ctx.posting
            .post(&common::deposit(
                cash,
                account,
                300_000000,
                &format!("dep-{i}-{}", Uuid::new_v4()),
            ))
            .await
            .unwrap();
    }

    // 500 de una vez al bolsillo: más que el tope por operación, y pasa.
    ctx.posting
        .post(&pocket_move(account, pocket.id, 500_000000))
        .await
        .unwrap();
    assert_eq!(
        ctx.accounts.balance_micros(pocket.id).await.unwrap(),
        500_000000
    );

    // 900 entre los dos; un depósito de 200 al bolsillo los llevaría a 1.100.
    let over = ctx
        .posting
        .post(&common::deposit(
            cash,
            pocket.id,
            200_000000,
            &format!("dep-x-{}", Uuid::new_v4()),
        ))
        .await
        .unwrap_err();
    assert!(
        matches!(over, PostingError::BalanceCapExceeded(_)),
        "fue: {over:?}"
    );
}

// La etiqueta de bolsillo no sirve para mandar dinero a otro saltándose el tope.
#[sqlx::test]
async fn un_movimiento_de_bolsillo_no_sale_de_la_familia(pool: PgPool) {
    let ctx = common::from_pool(pool);
    let product = ctx.simple_product(None, Some(300_000000)).await;
    let (cash, account) = ctx.cash_and_customer(&product).await;
    let stranger = ctx.customer_account(&product).await;
    ctx.posting
        .post(&common::deposit(
            cash,
            account,
            200_000000,
            &format!("dep-{}", Uuid::new_v4()),
        ))
        .await
        .unwrap();

    let err = ctx
        .posting
        .post(&pocket_move(account, stranger, 100_000000))
        .await
        .unwrap_err();
    assert!(matches!(err, PostingError::Invalid(_)), "fue: {err:?}");
    assert_eq!(ctx.accounts.balance_micros(stranger).await.unwrap(), 0);
}

#[sqlx::test]
async fn un_bolsillo_no_queda_en_negativo(pool: PgPool) {
    let ctx = common::from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (_, account) = ctx.cash_and_customer(&product).await;
    let pocket = ctx
        .accounts
        .open_pocket(account, &format!("pk-{}", Uuid::new_v4()), "Vacío")
        .await
        .unwrap();
    let err = ctx
        .posting
        .post(&pocket_move(pocket.id, account, 1_000000))
        .await
        .unwrap_err();
    assert!(
        matches!(err, PostingError::InsufficientFunds(_)),
        "fue: {err:?}"
    );
}

// Cerrar nunca hace desaparecer dinero: con saldo no se cierra.
#[sqlx::test]
async fn un_bolsillo_solo_se_cierra_vacio(pool: PgPool) {
    let ctx = common::from_pool(pool);
    let product = ctx.simple_product(None, None).await;
    let (cash, account) = ctx.cash_and_customer(&product).await;
    let pocket = ctx
        .accounts
        .open_pocket(account, &format!("pk-{}", Uuid::new_v4()), "Regalo")
        .await
        .unwrap();
    ctx.posting
        .post(&common::deposit(
            cash,
            account,
            50_000000,
            &format!("dep-{}", Uuid::new_v4()),
        ))
        .await
        .unwrap();
    ctx.posting
        .post(&pocket_move(account, pocket.id, 50_000000))
        .await
        .unwrap();

    let err = ctx.accounts.close_pocket(pocket.id).await.unwrap_err();
    assert!(matches!(err, PostingError::Invalid(_)), "fue: {err:?}");

    ctx.posting
        .post(&pocket_move(pocket.id, account, 50_000000))
        .await
        .unwrap();
    let closed = ctx.accounts.close_pocket(pocket.id).await.unwrap();
    assert_eq!(closed.status, AccountStatus::Closed);

    // Una cuenta que no es bolsillo no se cierra por aquí.
    let not_pocket = ctx.accounts.close_pocket(account).await.unwrap_err();
    assert!(
        matches!(not_pocket, PostingError::Invalid(_)),
        "fue: {not_pocket:?}"
    );
}

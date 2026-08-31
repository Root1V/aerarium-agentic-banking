//! Reglas de cuentas y productos:
//!
//! 1. Una cuenta de cliente hereda moneda y reglas del producto.
//! 2. Sin sobregiro: el saldo de un cliente nunca queda negativo — ni bajo concurrencia.
//! 3. Topes regulatorios del producto (saldo y monto por operación) se respetan.
//! 4. El saldo materializado coincide siempre con la proyección desde los asientos.
//! 5. Las cuentas internas sí admiten posición negativa.
//! 6. Transferencias cruzadas simultáneas no se bloquean entre sí (sin deadlock).

#![allow(clippy::inconsistent_digit_grouping)] // 100_00 = 100.00 en centavos

mod common;

use aibank_core::{AccountOwner, AccountType, PostingError};
use common::*;
use std::sync::Arc;
use uuid::Uuid;

// ------------------------------------------------------- 1. producto → cuenta

#[tokio::test]
async fn una_cuenta_de_cliente_hereda_moneda_y_reglas_del_producto() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let code = format!("cust-{}", Uuid::new_v4());

    let account = ctx
        .accounts
        .open_customer_account(&code, "Cliente", Uuid::new_v4(), &product)
        .await
        .expect("open account");

    assert_eq!(account.currency, product.currency);
    assert_eq!(account.product_id, Some(product.id));
    assert_eq!(account.account_type, AccountType::Liability, "el saldo del cliente es pasivo del banco");
    assert_eq!(account.owner, AccountOwner::Customer);
    assert!(!account.allows_overdraft, "una cuenta simple no admite sobregiro");
    assert_eq!(ctx.accounts.balance_minor(account.id).await.unwrap(), 0, "nace en cero");
}

// ------------------------------------------------------- 2. sin sobregiro

#[tokio::test]
async fn un_retiro_mayor_al_saldo_es_rechazado() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting.post(&deposit(cash, customer, 100_00, &format!("dep-{}", Uuid::new_v4())))
        .await.unwrap();

    let result = ctx.posting
        .post(&withdrawal(cash, customer, 150_00, &format!("wd-{}", Uuid::new_v4())))
        .await;

    assert!(
        matches!(result, Err(PostingError::InsufficientFunds(_))),
        "se esperaba InsufficientFunds, se obtuvo {result:?}"
    );
    assert_eq!(
        ctx.accounts.balance_minor(customer).await.unwrap(),
        100_00,
        "el rechazo no deja rastro: el saldo queda intacto"
    );
}

#[tokio::test]
async fn bajo_concurrencia_no_se_puede_sobregirar_la_cuenta() {
    let ctx = Arc::new(setup().await);
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting.post(&deposit(cash, customer, 100_00, &format!("dep-{}", Uuid::new_v4())))
        .await.unwrap();

    // 10 retiros simultáneos de 60.00 sobre un saldo de 100.00: solo uno cabe.
    let barrier = Arc::new(tokio::sync::Barrier::new(10));
    let mut handles = Vec::new();
    for _ in 0..10 {
        let ctx = Arc::clone(&ctx);
        let barrier = Arc::clone(&barrier);
        handles.push(tokio::spawn(async move {
            barrier.wait().await;
            ctx.posting
                .post(&withdrawal(cash, customer, 60_00, &format!("wd-{}", Uuid::new_v4())))
                .await
        }));
    }

    let mut ok = 0;
    let mut rejected = 0;
    for handle in handles {
        match handle.await.unwrap() {
            Ok(_) => ok += 1,
            Err(PostingError::InsufficientFunds(_)) => rejected += 1,
            Err(other) => panic!("error inesperado: {other:?}"),
        }
    }

    assert_eq!(ok, 1, "exactamente un retiro cabe en el saldo");
    assert_eq!(rejected, 9);
    assert_eq!(ctx.accounts.balance_minor(customer).await.unwrap(), 40_00);
}

#[tokio::test]
async fn una_cuenta_interna_si_admite_posicion_negativa() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, _customer) = ctx.cash_and_customer(&product).await;

    let expense = ctx
        .accounts
        .create_internal(&format!("exp-{}", Uuid::new_v4()), "Gastos operativos", AccountType::Expense, "USD")
        .await
        .unwrap();

    // El banco paga un gasto desde una caja sin fondos: la posición propia queda
    // en descubierto, lo que es válido porque no es dinero de un cliente.
    ctx.posting
        .post(&transfer(expense.id, cash, 10_00, &format!("exp-{}", Uuid::new_v4())))
        .await
        .expect("una cuenta interna puede quedar negativa");

    assert_eq!(ctx.accounts.balance_minor(cash).await.unwrap(), -10_00, "caja en descubierto");
    assert_eq!(ctx.accounts.balance_minor(expense.id).await.unwrap(), 10_00, "el gasto se registra");
}

// ------------------------------------------------------- 3. topes del producto

#[tokio::test]
async fn el_tope_de_saldo_del_producto_es_respetado() {
    let ctx = setup().await;
    // Cuenta simplificada: tope de saldo de 500.00
    let product = ctx.simple_product(Some(500_00), None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    ctx.posting.post(&deposit(cash, customer, 400_00, &format!("dep-{}", Uuid::new_v4())))
        .await.expect("cabe bajo el tope");

    let result = ctx.posting
        .post(&deposit(cash, customer, 200_00, &format!("dep-{}", Uuid::new_v4())))
        .await;

    assert!(
        matches!(result, Err(PostingError::BalanceCapExceeded(_))),
        "se esperaba BalanceCapExceeded, se obtuvo {result:?}"
    );
    assert_eq!(ctx.accounts.balance_minor(customer).await.unwrap(), 400_00);
}

#[tokio::test]
async fn el_tope_por_operacion_del_producto_es_respetado() {
    let ctx = setup().await;
    // Tope por operación: 100.00
    let product = ctx.simple_product(None, Some(100_00)).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;

    let result = ctx.posting
        .post(&deposit(cash, customer, 250_00, &format!("dep-{}", Uuid::new_v4())))
        .await;

    assert!(
        matches!(result, Err(PostingError::TransactionCapExceeded(_))),
        "se esperaba TransactionCapExceeded, se obtuvo {result:?}"
    );
    assert_eq!(ctx.accounts.balance_minor(customer).await.unwrap(), 0);
}

// ------------------------------------------------------- 4. saldo == proyección

#[tokio::test]
async fn el_saldo_materializado_coincide_con_la_proyeccion_del_ledger() {
    let ctx = setup().await;
    let product = ctx.simple_product(None, None).await;
    let (cash, customer) = ctx.cash_and_customer(&product).await;
    let other = ctx.customer_account(&product).await;

    ctx.posting.post(&deposit(cash, customer, 300_00, &format!("dep-{}", Uuid::new_v4()))).await.unwrap();
    ctx.posting.post(&transfer(customer, other, 120_00, &format!("xf-{}", Uuid::new_v4()))).await.unwrap();
    ctx.posting.post(&withdrawal(cash, customer, 50_00, &format!("wd-{}", Uuid::new_v4()))).await.unwrap();

    for account in [cash, customer, other] {
        let materialized = ctx.accounts.balance(account).await.unwrap();
        let projected = ctx.accounts.projected_balance(account).await.unwrap();
        assert_eq!(
            materialized, projected,
            "el saldo materializado debe reconstruirse exactamente desde los asientos (cuenta {account})"
        );
    }

    assert_eq!(ctx.accounts.balance_minor(customer).await.unwrap(), 130_00);
    assert_eq!(ctx.accounts.balance_minor(other).await.unwrap(), 120_00);
}

// ------------------------------------------------------- 6. sin deadlock

#[tokio::test]
async fn transferencias_cruzadas_simultaneas_no_producen_deadlock() {
    let ctx = Arc::new(setup().await);
    let product = ctx.simple_product(None, None).await;
    let (cash, alice) = ctx.cash_and_customer(&product).await;
    let bob = ctx.customer_account(&product).await;

    ctx.posting.post(&deposit(cash, alice, 1_000_00, &format!("dep-{}", Uuid::new_v4()))).await.unwrap();
    ctx.posting.post(&deposit(cash, bob, 1_000_00, &format!("dep-{}", Uuid::new_v4()))).await.unwrap();

    // 20 transferencias simultáneas en direcciones opuestas entre las mismas dos
    // cuentas: sin orden determinista de bloqueo, esto deadlockea.
    let barrier = Arc::new(tokio::sync::Barrier::new(20));
    let mut handles = Vec::new();
    for i in 0..20 {
        let ctx = Arc::clone(&ctx);
        let barrier = Arc::clone(&barrier);
        let (from, to) = if i % 2 == 0 { (alice, bob) } else { (bob, alice) };
        handles.push(tokio::spawn(async move {
            barrier.wait().await;
            ctx.posting.post(&transfer(from, to, 10_00, &format!("xf-{}", Uuid::new_v4()))).await
        }));
    }

    for handle in handles {
        handle.await.unwrap().expect("ninguna transferencia debe fallar por deadlock");
    }

    assert_eq!(ctx.accounts.balance_minor(alice).await.unwrap(), 1_000_00, "10 idas y 10 vueltas se compensan");
    assert_eq!(ctx.accounts.balance_minor(bob).await.unwrap(), 1_000_00);
}

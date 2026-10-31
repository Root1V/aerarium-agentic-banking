//! El ciclo de la tarjeta:
//!
//! 1. Pagar el total a tiempo es no pagar intereses (periodo de gracia).
//! 2. Lo que queda sin pagar al vencimiento genera intereses, con la TEA a 360 días.
//! 3. Sin el mínimo al vencimiento, la línea se bloquea: se paga, no se usa.
//!    Los intereses se cobran igual, aunque pasen el límite. Al ponerse al día, se
//!    desbloquea.
//! 4. El estado de cuenta es un documento: cuadra y no cambia.
//! 5. El trabajo diario corta cada línea en su día, una sola vez.

#![allow(clippy::inconsistent_digit_grouping)] // 100_000000 = 100,00 en micras (10^-6)

mod common;

use aibank_core::credit_cycle::interest_for;
use aibank_core::{
    CreditLineStatus, CreditRepository, NewCreditLine, PostingError, PostingRequest,
};
use chrono::{DateTime, Datelike, Days, Duration, Utc};
use common::*;
use uuid::Uuid;

struct Card {
    credit: CreditRepository,
    line: Uuid,
    account: Uuid,
    deposit: Uuid,
}

async fn card(ctx: &Ctx, limit: i64, cycle_day: i16) -> Card {
    let credit = CreditRepository::new(ctx.pool.clone());
    let line = credit
        .open(&NewCreditLine {
            customer_id: Uuid::new_v4(),
            name: "Ana Pérez".into(),
            currency: "USD".into(),
            limit_micros: limit,
            annual_rate_bps: 4500,
            cycle_day,
            payment_days: 20,
            decision_ref: format!("decision-{}", Uuid::new_v4()),
        })
        .await
        .expect("abrir línea");
    let product = ctx.simple_product(None, None).await;
    let (cash, deposit) = ctx.cash_and_customer(&product).await;
    ctx.posting
        .post(&deposit_req(cash, deposit, 2_000_000000))
        .await
        .expect("fondear");
    Card {
        credit,
        line: line.id,
        account: line.account_id,
        deposit,
    }
}

fn deposit_req(cash: Uuid, account: Uuid, amount: i64) -> PostingRequest {
    deposit(cash, account, amount, &format!("dep-{}", Uuid::new_v4()))
}

impl Card {
    async fn draw(&self, ctx: &Ctx, amount: i64) -> Result<(), PostingError> {
        let mut req = transfer(
            self.account,
            self.deposit,
            amount,
            &format!("draw-{}", Uuid::new_v4()),
        );
        req.kind = "credit_drawdown".into();
        ctx.posting.post(&req).await.map(|_| ())
    }

    async fn pay(&self, ctx: &Ctx, amount: i64) {
        let mut req = transfer(
            self.deposit,
            self.account,
            amount,
            &format!("pay-{}", Uuid::new_v4()),
        );
        req.kind = "credit_payment".into();
        ctx.posting.post(&req).await.expect("pagar");
    }

    async fn outstanding(&self) -> i64 {
        self.credit
            .find(self.line)
            .await
            .unwrap()
            .unwrap()
            .outstanding_micros
    }
}

/// El día siguiente al vencimiento de un estado de cuenta.
fn after_due(due: chrono::NaiveDate) -> DateTime<Utc> {
    (due + Days::new(1))
        .and_hms_opt(12, 0, 0)
        .unwrap()
        .and_utc()
}

#[tokio::test]
async fn pagar_el_total_a_tiempo_es_no_pagar_intereses() {
    let ctx = setup().await;
    let c = card(&ctx, 500_000000, 5).await;
    c.draw(&ctx, 200_000000).await.unwrap();

    let first = c.credit.close_cycle(c.line, Utc::now()).await.unwrap();
    assert_eq!(
        (
            first.opening_micros,
            first.charges_micros,
            first.closing_micros
        ),
        (0, 200_000000, 200_000000)
    );
    assert_eq!(
        first.interest_micros, 0,
        "el primer corte no tiene saldo anterior"
    );
    assert_eq!(first.minimum_payment_micros, 10_000000, "5 % de 200 = 10");
    assert_eq!(
        first.due_date,
        first.period_end.date_naive() + Days::new(20)
    );

    c.pay(&ctx, 200_000000).await;
    let second = c
        .credit
        .close_cycle(c.line, first.period_end + Duration::days(30))
        .await
        .unwrap();
    assert_eq!(second.interest_micros, 0, "pagó todo antes del vencimiento");
    assert_eq!(
        (
            second.payments_micros,
            second.closing_micros,
            second.minimum_payment_micros
        ),
        (200_000000, 0, 0)
    );
}

#[tokio::test]
async fn lo_que_queda_sin_pagar_genera_intereses() {
    let ctx = setup().await;
    let c = card(&ctx, 500_000000, 5).await;
    c.draw(&ctx, 300_000000).await.unwrap();
    let first = c.credit.close_cycle(c.line, Utc::now()).await.unwrap();

    c.pay(&ctx, 50_000000).await;
    let second = c
        .credit
        .close_cycle(c.line, first.period_end + Duration::days(30))
        .await
        .unwrap();

    let expected = interest_for(250_000000, 4500, 30);
    assert!(expected > 7_000000 && expected < 8_000000, "{expected}");
    assert_eq!(
        second.interest_micros, expected,
        "intereses sobre los 250 no pagados, 30 días"
    );
    assert_eq!(second.closing_micros, 300_000000 - 50_000000 + expected);
    assert_eq!(
        c.outstanding().await,
        second.closing_micros,
        "el estado de cuenta cuadra con la deuda"
    );
    // El mínimo cubre los intereses más el 5 %.
    let share = (second.closing_micros * 500 + 9_999) / 10_000;
    assert_eq!(second.minimum_payment_micros, expected + share);
}

#[tokio::test]
async fn sin_el_minimo_se_bloquea_los_intereses_se_cobran_igual_y_al_pagar_se_desbloquea() {
    let ctx = setup().await;
    let c = card(&ctx, 300_000000, 5).await;
    c.draw(&ctx, 300_000000).await.unwrap();
    let first = c.credit.close_cycle(c.line, Utc::now()).await.unwrap();

    // Antes del vencimiento, no pagar todavía no es mora.
    let line = c
        .credit
        .review(c.line, first.period_end + Duration::days(5))
        .await
        .unwrap();
    assert_eq!(line.status, CreditLineStatus::Active);

    let line = c
        .credit
        .review(c.line, after_due(first.due_date))
        .await
        .unwrap();
    assert_eq!(
        line.status,
        CreditLineStatus::Blocked,
        "pasó el vencimiento sin el mínimo"
    );
    c.pay(&ctx, 1_000000).await; // el pago se aceptaría aunque esté bloqueada
    assert!(matches!(
        c.draw(&ctx, 1_000000).await,
        Err(PostingError::CreditLineInactive(_))
    ));

    // Los intereses se cobran aunque la línea esté bloqueada y la deuda pase del límite.
    let second = c
        .credit
        .close_cycle(c.line, first.period_end + Duration::days(30))
        .await
        .unwrap();
    assert!(second.interest_micros > 0);
    assert!(
        c.outstanding().await > 300_000000,
        "con intereses, por encima del límite"
    );

    // Pagar lo que faltaba del mínimo del estado vencido la pone al día.
    c.pay(&ctx, first.minimum_payment_micros).await;
    let line = c
        .credit
        .review(c.line, after_due(first.due_date))
        .await
        .unwrap();
    assert_eq!(line.status, CreditLineStatus::Active);
}

#[tokio::test]
async fn el_estado_de_cuenta_es_un_documento() {
    let ctx = setup().await;
    let c = card(&ctx, 500_000000, 5).await;
    c.draw(&ctx, 100_000000).await.unwrap();
    let first = c.credit.close_cycle(c.line, Utc::now()).await.unwrap();

    assert!(
        matches!(
            c.credit.close_cycle(c.line, first.period_end).await,
            Err(PostingError::Invalid(_))
        ),
        "no se corta dos veces el mismo instante ni hacia atrás"
    );
    let edited = sqlx::query("UPDATE credit_statements SET closing_micros = 0 WHERE id = $1")
        .bind(first.id)
        .execute(&ctx.pool)
        .await;
    assert!(edited.is_err(), "un estado de cuenta emitido no se edita");
}

#[tokio::test]
async fn el_trabajo_diario_corta_cada_linea_en_su_dia_una_vez() {
    let ctx = setup().await;
    // Un día de corte que no usa ninguna otra prueba de este archivo: el corte
    // diario corta TODAS las líneas de ese día, y estas pruebas corren en paralelo
    // sobre la misma base. El próximo 17 desde mañana: la línea ya existe ese día.
    let mut day = Utc::now() + Duration::days(1);
    while day.day() != 17 {
        day += Duration::days(1);
    }
    let c = card(&ctx, 500_000000, 17).await;
    c.draw(&ctx, 100_000000).await.unwrap();

    // Solo el corte: revisar la mora de TODAS las líneas tocaría las de otras
    // pruebas que corren en paralelo sobre la misma base.
    c.credit.cut_due(day).await.unwrap();
    let after_first = c.credit.statements(c.line, 10).await.unwrap();
    assert_eq!(after_first.len(), 1, "se cortó en su día");
    assert_eq!(after_first[0].closing_micros, 100_000000);

    c.credit.cut_due(day + Duration::hours(3)).await.unwrap();
    assert_eq!(
        c.credit.statements(c.line, 10).await.unwrap().len(),
        1,
        "una sola vez por día"
    );
}

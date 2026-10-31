//! El ciclo de la tarjeta: corte, intereses, pago mínimo, vencimiento y mora.
//!
//! **Intereses.** Si al vencimiento se pagó todo el estado de cuenta anterior, no
//! hay intereses: es el periodo de gracia. Si no, el saldo que quedó sin pagar
//! genera intereses por los días del periodo, con la tasa efectiva anual del
//! contrato convertida a días con año de 360, como se expresa la TEA en Perú:
//!
//! ```text
//! interés = no_pagado × ((1 + TEA)^(días / 360) − 1)
//! ```
//!
//! Es deliberadamente más simple que el saldo promedio diario: es explicable en
//! una línea del estado de cuenta, y es exacto para el caso que más importa, que
//! es quien no pagó lo que debía.
//!
//! **Pago mínimo.** Los intereses del periodo más un porcentaje de lo que se debe
//! al corte, con un piso, y nunca más que el total.
//!
//! **Mora.** Si al pasar el vencimiento no se pagó el mínimo, la línea se bloquea:
//! se puede pagar, no usar. Se desbloquea sola al ponerse al día.

use crate::credit::{CreditLine, CreditLineStatus, CreditRepository};
use crate::model::*;
use crate::posting::post_in_tx;
use chrono::{DateTime, Days, NaiveDate, Utc};
use uuid::Uuid;

/// Un estado de cuenta emitido.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Statement {
    pub id: Uuid,
    pub line_id: Uuid,
    pub currency: String,
    pub period_start: DateTime<Utc>,
    pub period_end: DateTime<Utc>,
    pub opening_micros: i64,
    pub charges_micros: i64,
    pub payments_micros: i64,
    pub interest_micros: i64,
    pub closing_micros: i64,
    pub minimum_payment_micros: i64,
    pub due_date: NaiveDate,
    /// Lo pagado desde el corte, hasta hoy. Se calcula al consultar.
    pub paid_since_cut_micros: i64,
}

impl Statement {
    /// Si ya se pagó lo mínimo.
    pub fn minimum_covered(&self) -> bool {
        self.paid_since_cut_micros >= self.minimum_payment_micros
    }
}

/// Intereses de un saldo no pagado durante `days` días a una TEA en puntos básicos.
pub fn interest_for(unpaid_micros: i64, annual_rate_bps: i32, days: i64) -> i64 {
    if unpaid_micros <= 0 || annual_rate_bps <= 0 || days <= 0 {
        return 0;
    }
    let rate = annual_rate_bps as f64 / 10_000.0;
    let factor = (1.0 + rate).powf(days as f64 / 360.0) - 1.0;
    (unpaid_micros as f64 * factor).round() as i64
}

/// Pago mínimo: intereses más un porcentaje de lo que se debe, con piso, sin pasar
/// del total.
pub fn minimum_payment(
    closing_micros: i64,
    interest_micros: i64,
    bps: i32,
    floor_micros: i64,
) -> i64 {
    if closing_micros <= 0 {
        return 0;
    }
    // Redondeo hacia arriba: el mínimo nunca puede quedarse una micra corto.
    let share = ((closing_micros as i128 * bps as i128 + 9_999) / 10_000) as i64;
    (interest_micros + share)
        .max(floor_micros)
        .min(closing_micros)
}

struct LineTerms {
    account_id: Uuid,
    currency: String,
    annual_rate_bps: i32,
    payment_days: i16,
    min_payment_bps: i32,
    min_payment_floor_micros: i64,
    opened_at: DateTime<Utc>,
}

impl CreditRepository {
    /// Corta el estado de cuenta de la línea en `cut_at`: cobra los intereses que
    /// correspondan y emite el estado de cuenta, todo en una transacción.
    pub async fn close_cycle(
        &self,
        line_id: Uuid,
        cut_at: DateTime<Utc>,
    ) -> Result<Statement, PostingError> {
        let mut tx = self.pool().begin().await.map_err(PostingError::from_db)?;

        // El bloqueo de la línea serializa dos cortes simultáneos de la misma.
        let terms = sqlx::query_as!(
            LineTerms,
            r#"
            SELECT l.account_id, a.currency, l.annual_rate_bps, l.payment_days,
                   l.min_payment_bps, l.min_payment_floor_micros, l.opened_at
            FROM credit_lines l JOIN accounts a ON a.id = l.account_id
            WHERE l.id = $1 AND l.status <> 'CLOSED'
            FOR UPDATE OF l
            "#,
            line_id,
        )
        .fetch_optional(&mut *tx)
        .await
        .map_err(PostingError::from_db)?
        .ok_or_else(|| {
            PostingError::Invalid(format!("credit line {line_id} not found or closed"))
        })?;

        let previous = sqlx::query!(
            r#"
            SELECT period_end, closing_micros, due_date
            FROM credit_statements WHERE line_id = $1
            ORDER BY period_end DESC LIMIT 1
            "#,
            line_id,
        )
        .fetch_optional(&mut *tx)
        .await
        .map_err(PostingError::from_db)?;

        let period_start = previous
            .as_ref()
            .map(|p| p.period_end)
            .unwrap_or(terms.opened_at);
        if cut_at <= period_start {
            return Err(PostingError::Invalid(format!(
                "cut {cut_at} must be after the period start {period_start}"
            )));
        }
        let opening = previous.as_ref().map(|p| p.closing_micros).unwrap_or(0);

        // Lo movido en el periodo. Los intereses no cuentan como uso: van en su
        // propia línea del estado de cuenta que los cobró.
        let moved = sqlx::query!(
            r#"
            SELECT
              COALESCE(SUM(e.amount_micros) FILTER (WHERE e.direction = 'DEBIT' AND t.kind <> 'credit_interest'), 0)::BIGINT as "charges!",
              COALESCE(SUM(e.amount_micros) FILTER (WHERE e.direction = 'CREDIT'), 0)::BIGINT as "payments!"
            FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
            WHERE e.account_id = $1 AND t.posted_at > $2 AND t.posted_at <= $3
            "#,
            terms.account_id,
            period_start,
            cut_at,
        )
        .fetch_one(&mut *tx)
        .await
        .map_err(PostingError::from_db)?;

        // Intereses: solo sobre lo que quedó sin pagar del estado anterior a su
        // vencimiento. Pagar el total a tiempo es no pagar intereses.
        let mut interest = 0;
        if let Some(prev) = &previous {
            let due_end = end_of_day(prev.due_date);
            let paid_by_due = sqlx::query_scalar!(
                r#"
                SELECT COALESCE(SUM(e.amount_micros), 0)::BIGINT as "paid!"
                FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
                WHERE e.account_id = $1 AND e.direction = 'CREDIT'
                  AND t.posted_at > $2 AND t.posted_at <= $3
                "#,
                terms.account_id,
                prev.period_end,
                due_end,
            )
            .fetch_one(&mut *tx)
            .await
            .map_err(PostingError::from_db)?;
            let unpaid = (prev.closing_micros - paid_by_due).max(0);
            let days = (cut_at - period_start).num_days();
            interest = interest_for(unpaid, terms.annual_rate_bps, days);
        }

        let mut interest_tx = None;
        if interest > 0 {
            let income = interest_income_account(&mut tx, &terms.currency).await?;
            let mut request = PostingRequest::new(
                format!("credit-interest:{line_id}:{}", cut_at.timestamp_micros()),
                "credit_interest",
                vec![
                    EntryCommand::debit(terms.account_id, interest, &terms.currency),
                    EntryCommand::credit(income, interest, &terms.currency),
                ],
            );
            request.description = Some("intereses del periodo".into());
            let posted = post_in_tx(&mut tx, &request).await?;
            interest_tx = Some(posted.transaction.id);
        }

        let closing = opening + moved.charges + interest - moved.payments;
        let minimum = minimum_payment(
            closing,
            interest,
            terms.min_payment_bps,
            terms.min_payment_floor_micros,
        );
        let due_date = cut_at.date_naive() + Days::new(terms.payment_days as u64);

        let id = sqlx::query_scalar!(
            r#"
            INSERT INTO credit_statements (line_id, period_start, period_end, opening_micros, charges_micros,
                payments_micros, interest_micros, closing_micros, minimum_payment_micros, due_date,
                interest_transaction_id)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
            RETURNING id
            "#,
            line_id,
            period_start,
            cut_at,
            opening,
            moved.charges,
            moved.payments,
            interest,
            closing,
            minimum,
            due_date,
            interest_tx,
        )
        .fetch_one(&mut *tx)
        .await
        .map_err(PostingError::from_db)?;

        tx.commit().await.map_err(PostingError::from_db)?;
        self.statement(id).await.map_err(PostingError::from_db)
    }

    /// Los estados de cuenta de la línea, del más reciente al más antiguo.
    pub async fn statements(
        &self,
        line_id: Uuid,
        limit: i64,
    ) -> Result<Vec<Statement>, sqlx::Error> {
        let rows = sqlx::query_as!(
            StatementRow,
            r#"
            SELECT s.id, s.line_id, a.currency, s.period_start, s.period_end, s.opening_micros,
                   s.charges_micros, s.payments_micros, s.interest_micros, s.closing_micros,
                   s.minimum_payment_micros, s.due_date,
                   (SELECT COALESCE(SUM(e.amount_micros), 0)
                      FROM ledger_entries e JOIN ledger_transactions t ON t.id = e.transaction_id
                     WHERE e.account_id = l.account_id AND e.direction = 'CREDIT'
                       AND t.posted_at > s.period_end)::BIGINT as "paid_since_cut_micros!"
            FROM credit_statements s
            JOIN credit_lines l ON l.id = s.line_id
            JOIN accounts a ON a.id = l.account_id
            WHERE s.line_id = $1
            ORDER BY s.period_end DESC
            LIMIT $2
            "#,
            line_id,
            limit,
        )
        .fetch_all(self.pool())
        .await?;
        Ok(rows.into_iter().map(Into::into).collect())
    }

    async fn statement(&self, id: Uuid) -> Result<Statement, sqlx::Error> {
        let line_id =
            sqlx::query_scalar!("SELECT line_id FROM credit_statements WHERE id = $1", id)
                .fetch_one(self.pool())
                .await?;
        self.statements(line_id, 50)
            .await?
            .into_iter()
            .find(|s| s.id == id)
            .ok_or(sqlx::Error::RowNotFound)
    }

    /// Revisa la mora de la línea a la fecha `as_of`.
    ///
    /// Manda el último estado de cuenta ya vencido: si desde su corte se pagó al
    /// menos su mínimo, la línea está al día; si no, se bloquea. Bloquear o
    /// desbloquear es lo único que cambia.
    pub async fn review(
        &self,
        line_id: Uuid,
        as_of: DateTime<Utc>,
    ) -> Result<CreditLine, PostingError> {
        let due = self
            .statements(line_id, 50)
            .await
            .map_err(PostingError::from_db)?
            .into_iter()
            .find(|s| end_of_day(s.due_date) < as_of);
        let behind = due.as_ref().is_some_and(|s| !s.minimum_covered());
        let status = if behind {
            CreditLineStatus::Blocked
        } else {
            CreditLineStatus::Active
        };

        sqlx::query!(
            r#"
            UPDATE credit_lines SET status = $2
             WHERE id = $1 AND status <> 'CLOSED' AND status <> $2
            "#,
            line_id,
            status as CreditLineStatus,
        )
        .execute(self.pool())
        .await
        .map_err(PostingError::from_db)?;

        self.find(line_id)
            .await
            .map_err(PostingError::from_db)?
            .ok_or_else(|| PostingError::Invalid(format!("credit line {line_id} not found")))
    }

    /// El trabajo del día: cortar lo que toca y revisar la mora de todas.
    /// Devuelve cuántas cortó.
    pub async fn run_due(&self, now: DateTime<Utc>) -> Result<usize, PostingError> {
        let cut = self.cut_due(now).await?;
        self.review_all(now).await?;
        Ok(cut)
    }

    /// Corta las líneas cuyo día de corte es hoy y que todavía no se cortaron.
    pub async fn cut_due(&self, now: DateTime<Utc>) -> Result<usize, PostingError> {
        let today = now.date_naive();
        let to_cut = sqlx::query_scalar!(
            r#"
            SELECT l.id FROM credit_lines l
            WHERE l.status <> 'CLOSED'
              AND l.cycle_day = EXTRACT(DAY FROM $1::date)::SMALLINT
              AND l.opened_at::date < $1::date
              AND NOT EXISTS (SELECT 1 FROM credit_statements s
                               WHERE s.line_id = l.id AND s.period_end::date = $1::date)
            "#,
            today,
        )
        .fetch_all(self.pool())
        .await
        .map_err(PostingError::from_db)?;

        let mut cut = 0;
        for line_id in &to_cut {
            match self.close_cycle(*line_id, now).await {
                Ok(_) => cut += 1,
                // Una línea que falla no frena las demás; se reintenta mañana.
                Err(err) => {
                    tracing::error!(%line_id, error = %err, "no se pudo cortar el estado de cuenta")
                }
            }
        }
        Ok(cut)
    }

    /// Revisa la mora de todas las líneas con estado de cuenta.
    pub async fn review_all(&self, now: DateTime<Utc>) -> Result<(), PostingError> {
        let open = sqlx::query_scalar!(
            r#"SELECT DISTINCT line_id FROM credit_statements s
               JOIN credit_lines l ON l.id = s.line_id WHERE l.status <> 'CLOSED'"#
        )
        .fetch_all(self.pool())
        .await
        .map_err(PostingError::from_db)?;
        for line_id in open {
            if let Err(err) = self.review(line_id, now).await {
                tracing::error!(%line_id, error = %err, "no se pudo revisar la mora");
            }
        }
        Ok(())
    }
}

/// El final del día del vencimiento: se puede pagar hasta las 23:59:59.
fn end_of_day(date: NaiveDate) -> DateTime<Utc> {
    (date + Days::new(1))
        .and_hms_opt(0, 0, 0)
        .expect("medianoche válida")
        .and_utc()
}

/// La cuenta de ingresos por intereses, una por moneda; se crea la primera vez.
async fn interest_income_account(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    currency: &str,
) -> Result<Uuid, PostingError> {
    let code = format!("CREDIT-INTEREST-{currency}");
    sqlx::query!(
        r#"
        INSERT INTO accounts (code, name, type, owner, currency, allows_overdraft)
        VALUES ($1, $2, 'INCOME', 'INTERNAL', $3, TRUE)
        ON CONFLICT (code) DO NOTHING
        "#,
        code,
        format!("Intereses de tarjeta de crédito {currency}"),
        currency,
    )
    .execute(&mut **tx)
    .await
    .map_err(PostingError::from_db)?;
    sqlx::query_scalar!("SELECT id FROM accounts WHERE code = $1", code)
        .fetch_one(&mut **tx)
        .await
        .map_err(PostingError::from_db)
}

struct StatementRow {
    id: Uuid,
    line_id: Uuid,
    currency: String,
    period_start: DateTime<Utc>,
    period_end: DateTime<Utc>,
    opening_micros: i64,
    charges_micros: i64,
    payments_micros: i64,
    interest_micros: i64,
    closing_micros: i64,
    minimum_payment_micros: i64,
    due_date: NaiveDate,
    paid_since_cut_micros: i64,
}

impl From<StatementRow> for Statement {
    fn from(r: StatementRow) -> Self {
        Statement {
            id: r.id,
            line_id: r.line_id,
            currency: r.currency,
            period_start: r.period_start,
            period_end: r.period_end,
            opening_micros: r.opening_micros,
            charges_micros: r.charges_micros,
            payments_micros: r.payments_micros,
            interest_micros: r.interest_micros,
            closing_micros: r.closing_micros,
            minimum_payment_micros: r.minimum_payment_micros,
            due_date: r.due_date,
            paid_since_cut_micros: r.paid_since_cut_micros,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sin_saldo_o_sin_tasa_no_hay_intereses() {
        assert_eq!(interest_for(0, 4500, 30), 0);
        assert_eq!(interest_for(100_000000, 0, 30), 0);
        assert_eq!(interest_for(100_000000, 4500, 0), 0);
    }

    #[test]
    fn un_anio_de_360_dias_a_la_tea_da_la_tea() {
        // 100 a 45 % por 360 días son exactamente 45 de interés.
        assert_eq!(interest_for(100_000000, 4500, 360), 45_000000);
    }

    #[test]
    fn treinta_dias_al_45_por_ciento() {
        // 1,45^(30/360) − 1 = 0,031448... sobre 100 = 3,1448...
        let interest = interest_for(100_000000, 4500, 30);
        assert!((3_144_000..=3_146_000).contains(&interest), "{interest}");
    }

    #[test]
    fn el_minimo_cubre_intereses_mas_una_parte_con_piso_y_sin_pasar_del_total() {
        // 5 % de 400 = 20, más 3 de intereses.
        assert_eq!(
            minimum_payment(400_000000, 3_000000, 500, 10_000000),
            23_000000
        );
        // 5 % de 50 = 2,5: manda el piso de 10.
        assert_eq!(minimum_payment(50_000000, 0, 500, 10_000000), 10_000000);
        // Se deben 6: el mínimo no puede ser más que el total.
        assert_eq!(minimum_payment(6_000000, 0, 500, 10_000000), 6_000000);
        assert_eq!(minimum_payment(0, 0, 500, 10_000000), 0);
        // Redondea hacia arriba la micra.
        assert_eq!(minimum_payment(1_000001, 0, 5000, 0), 500_001);
    }
}

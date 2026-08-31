//! Motor de conciliación.
//!
//! Compara el ledger contra sí mismo y contra lo que reportan los proveedores.
//! Es la contrapartida indispensable de haber construido un ledger propio: sin él
//! no habría nada contra qué comparar, y una diferencia solo aparecería el día que
//! alguien reclama su dinero.
//!
//! Las claves de idempotencia son lo que hace posible el cruce con el exterior:
//! como cada movimiento originado por un proveedor lleva una clave derivada del
//! identificador de ESE proveedor, casar ambos registros es comparar claves.

use chrono::{DateTime, Duration, Utc};
use serde_json::json;
use sqlx::PgPool;
use uuid::Uuid;

#[derive(Debug, Clone, Copy, PartialEq, Eq, sqlx::Type)]
#[sqlx(type_name = "finding_kind", rename_all = "SCREAMING_SNAKE_CASE")]
pub enum FindingKind {
    /// El saldo materializado no coincide con la suma de los asientos.
    BalanceDrift,
    /// El proveedor registra un movimiento que no está en el ledger: puede
    /// significar un webhook perdido y un cliente sin acreditar.
    MissingInLedger,
    /// El ledger registra un movimiento que el proveedor no reporta: puede
    /// significar dinero acreditado sin respaldo real.
    MissingAtProvider,
    /// Ambos lo tienen, por importes distintos.
    AmountMismatch,
    /// Dinero detenido en tránsito o retenido más allá del plazo razonable.
    StaleSuspense,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Finding {
    /// Identidad del hallazgo. 0 mientras no se ha persistido.
    pub id: i64,
    pub run_id: Option<Uuid>,
    pub kind: FindingKind,
    pub account_id: Option<Uuid>,
    pub reference: Option<String>,
    pub expected_minor: Option<i64>,
    pub actual_minor: Option<i64>,
    pub currency: Option<String>,
    pub detail: String,
    pub created_at: Option<DateTime<Utc>>,
}

/// Movimiento tal como lo reporta un proveedor en su extracto.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ExternalMovement {
    /// Clave de idempotencia con la que el adaptador asentó (o debió asentar)
    /// este movimiento. Es el punto de cruce entre ambos registros.
    pub idempotency_key: String,
    pub amount_minor: i64,
    pub currency: String,
    /// Referencia legible del proveedor, para el informe.
    pub reference: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ReconciliationRun {
    pub id: Uuid,
    pub scope: String,
    pub findings: Vec<Finding>,
}

impl ReconciliationRun {
    /// Un cierre limpio: sin diferencias que resolver.
    pub fn is_clean(&self) -> bool {
        self.findings.is_empty()
    }

    pub fn count_of(&self, kind: FindingKind) -> usize {
        self.findings.iter().filter(|f| f.kind == kind).count()
    }
}

pub struct Reconciler {
    pool: PgPool,
}

impl Reconciler {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Conciliación interna: el ledger contra sí mismo.
    ///
    /// Detecta que el saldo materializado se haya separado de la suma de los
    /// asientos. Los triggers del esquema deberían hacerlo imposible — y esa es
    /// exactamente la razón de comprobarlo: la conciliación existe para cazar lo
    /// que "no puede pasar".
    pub async fn check_internal(&self) -> Result<ReconciliationRun, sqlx::Error> {
        let run_id = self.start_run("internal").await?;
        let findings = self.detect_balance_drift().await?;
        self.finish_run(run_id, &findings).await?;

        Ok(ReconciliationRun { id: run_id, scope: "internal".into(), findings })
    }

    /// Concilia una cuenta contra el extracto de un proveedor.
    ///
    /// El cruce es por clave de idempotencia: cada movimiento originado afuera se
    /// asentó con una clave derivada del identificador del proveedor, así que
    /// ambos registros hablan el mismo idioma sin necesidad de heurísticas de
    /// coincidencia por importe y fecha —que es como se cuelan los falsos cuadres.
    pub async fn check_against_provider(
        &self,
        scope: &str,
        account_id: Uuid,
        statement: &[ExternalMovement],
        period_start: DateTime<Utc>,
    ) -> Result<ReconciliationRun, sqlx::Error> {
        let run_id = self.start_run(scope).await?;

        let ledger = sqlx::query!(
            r#"
            SELECT t.idempotency_key, e.amount_minor, e.currency
            FROM ledger_entries e
            JOIN ledger_transactions t ON t.id = e.transaction_id
            WHERE e.account_id = $1
              AND t.posted_at >= $2
              AND t.idempotency_key LIKE $3
            "#,
            account_id,
            period_start,
            format!("{scope}:%"),
        )
        .fetch_all(&self.pool)
        .await?;

        let mut findings = Vec::new();
        let mut seen_keys = std::collections::HashSet::new();

        for movement in statement {
            seen_keys.insert(movement.idempotency_key.clone());

            match ledger
                .iter()
                .find(|row| row.idempotency_key == movement.idempotency_key)
            {
                None => findings.push(Finding {
                    id: 0,
                    run_id: None,
                    kind: FindingKind::MissingInLedger,
                    account_id: Some(account_id),
                    reference: Some(movement.reference.clone()),
                    expected_minor: Some(movement.amount_minor),
                    actual_minor: None,
                    currency: Some(movement.currency.clone()),
                    detail: format!(
                        "el proveedor reporta {} y no hay asiento: posible notificación perdida",
                        movement.reference
                    ),
                    created_at: None,
                }),
                Some(row) if row.amount_minor != movement.amount_minor => findings.push(Finding {
                    id: 0,
                    run_id: None,
                    kind: FindingKind::AmountMismatch,
                    account_id: Some(account_id),
                    reference: Some(movement.reference.clone()),
                    expected_minor: Some(movement.amount_minor),
                    actual_minor: Some(row.amount_minor),
                    currency: Some(movement.currency.clone()),
                    detail: format!("importes distintos para {}", movement.reference),
                    created_at: None,
                }),
                Some(_) => {}
            }
        }

        for row in &ledger {
            if !seen_keys.contains(&row.idempotency_key) {
                findings.push(Finding {
                    id: 0,
                    run_id: None,
                    kind: FindingKind::MissingAtProvider,
                    account_id: Some(account_id),
                    reference: Some(row.idempotency_key.clone()),
                    expected_minor: None,
                    actual_minor: Some(row.amount_minor),
                    currency: Some(row.currency.clone()),
                    detail: format!(
                        "hay asiento para {} y el proveedor no lo reporta: posible acreditación sin respaldo",
                        row.idempotency_key
                    ),
                    created_at: None,
                });
            }
        }

        self.finish_run(run_id, &findings).await?;
        Ok(ReconciliationRun { id: run_id, scope: scope.into(), findings })
    }

    /// Detecta dinero detenido en una cuenta de tránsito o retención.
    ///
    /// Esas cuentas existen justamente para hacer visible el limbo; sin un proceso
    /// que las vigile, el limbo se vuelve invisible otra vez.
    pub async fn check_stale_suspense(
        &self,
        scope: &str,
        suspense_account_id: Uuid,
        max_age: Duration,
    ) -> Result<ReconciliationRun, sqlx::Error> {
        let run_id = self.start_run(scope).await?;
        let cutoff = Utc::now() - max_age;

        // Un movimiento sigue "parado" si su contrapartida no llegó: se detecta
        // porque la cuenta conserva saldo y sus asientos más recientes son viejos.
        let rows = sqlx::query!(
            r#"
            SELECT t.idempotency_key, e.amount_minor, e.currency, t.posted_at
            FROM ledger_entries e
            JOIN ledger_transactions t ON t.id = e.transaction_id
            WHERE e.account_id = $1 AND t.posted_at < $2
            ORDER BY t.posted_at
            "#,
            suspense_account_id,
            cutoff,
        )
        .fetch_all(&self.pool)
        .await?;

        // Solo importa si la cuenta conserva saldo: si quedó en cero, todo lo que
        // entró ya salió y no hay nada detenido.
        let balance = sqlx::query!(
            r#"SELECT balance_minor as "balance_minor!" FROM account_balances WHERE account_id = $1"#,
            suspense_account_id,
        )
        .fetch_one(&self.pool)
        .await?
        .balance_minor;

        let mut findings = Vec::new();
        if balance != 0 && !rows.is_empty() {
            let oldest = rows.first().unwrap();
            findings.push(Finding {
                id: 0,
                run_id: None,
                kind: FindingKind::StaleSuspense,
                account_id: Some(suspense_account_id),
                reference: Some(oldest.idempotency_key.clone()),
                expected_minor: Some(0),
                actual_minor: Some(balance),
                currency: Some(oldest.currency.clone()),
                detail: format!(
                    "hay {} en la cuenta desde {}: requiere resolución manual",
                    balance, oldest.posted_at
                ),
                created_at: None,
            });
        }

        self.finish_run(run_id, &findings).await?;
        Ok(ReconciliationRun { id: run_id, scope: scope.into(), findings })
    }

    /// Diferencias abiertas, para la cola de operaciones.
    pub async fn open_findings(&self, limit: i64) -> Result<Vec<Finding>, sqlx::Error> {
        let rows = sqlx::query!(
            r#"
            SELECT id, run_id, kind as "kind: FindingKind", account_id, reference,
                   expected_minor, actual_minor, currency, detail, created_at
            FROM reconciliation_findings
            WHERE resolved_at IS NULL
            ORDER BY id DESC
            LIMIT $1
            "#,
            limit,
        )
        .fetch_all(&self.pool)
        .await?;

        Ok(rows
            .into_iter()
            .map(|r| Finding {
                id: r.id,
                run_id: Some(r.run_id),
                kind: r.kind,
                account_id: r.account_id,
                reference: r.reference,
                expected_minor: r.expected_minor,
                actual_minor: r.actual_minor,
                currency: r.currency,
                detail: r.detail,
                created_at: Some(r.created_at),
            })
            .collect())
    }

    /// Cierra un hallazgo concreto dejando constancia de cómo se resolvió.
    pub async fn resolve_finding(&self, finding_id: i64, resolution: &str) -> Result<bool, sqlx::Error> {
        let result = sqlx::query!(
            r#"
            UPDATE reconciliation_findings
            SET resolved_at = now(), resolution = $2
            WHERE id = $1 AND resolved_at IS NULL
            "#,
            finding_id,
            resolution,
        )
        .execute(&self.pool)
        .await?;
        Ok(result.rows_affected() == 1)
    }

    /// Cierra todas las diferencias de una corrida.
    pub async fn resolve(&self, run_id: Uuid, resolution: &str) -> Result<u64, sqlx::Error> {
        let result = sqlx::query!(
            r#"
            UPDATE reconciliation_findings
            SET resolved_at = now(), resolution = $2
            WHERE run_id = $1 AND resolved_at IS NULL
            "#,
            run_id,
            resolution,
        )
        .execute(&self.pool)
        .await?;
        Ok(result.rows_affected())
    }

    // ------------------------------------------------------------ internos

    async fn detect_balance_drift(&self) -> Result<Vec<Finding>, sqlx::Error> {
        // Una sola pasada compara todas las cuentas. Es un recorrido completo de
        // los asientos: aceptable a este volumen, pero al crecer habrá que
        // conciliar por ventanas con saldos de corte.
        let rows = sqlx::query!(
            r#"
            SELECT b.account_id,
                   b.balance_minor as "materialized!",
                   p.projected_minor as "projected!",
                   b.entry_count as "counted!",
                   p.projected_entries as "projected_entries!",
                   a.currency
            FROM account_balances b
            JOIN account_balance_projection p ON p.account_id = b.account_id
            JOIN accounts a ON a.id = b.account_id
            WHERE b.balance_minor <> p.projected_minor
               OR b.entry_count <> p.projected_entries
            "#
        )
        .fetch_all(&self.pool)
        .await?;

        Ok(rows
            .into_iter()
            .map(|r| Finding {
                id: 0,
                run_id: None,
                kind: FindingKind::BalanceDrift,
                account_id: Some(r.account_id),
                reference: None,
                expected_minor: Some(r.projected),
                actual_minor: Some(r.materialized),
                currency: Some(r.currency),
                detail: format!(
                    "saldo materializado {} contra {} de los asientos ({} vs {} movimientos)",
                    r.materialized, r.projected, r.counted, r.projected_entries
                ),
                created_at: None,
            })
            .collect())
    }

    async fn start_run(&self, scope: &str) -> Result<Uuid, sqlx::Error> {
        let row = sqlx::query!(
            "INSERT INTO reconciliation_runs (scope) VALUES ($1) RETURNING id",
            scope,
        )
        .fetch_one(&self.pool)
        .await?;
        Ok(row.id)
    }

    async fn finish_run(&self, run_id: Uuid, findings: &[Finding]) -> Result<(), sqlx::Error> {
        for finding in findings {
            sqlx::query!(
                r#"
                INSERT INTO reconciliation_findings
                    (run_id, kind, account_id, reference, expected_minor, actual_minor, currency, detail)
                VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
                "#,
                run_id,
                finding.kind as FindingKind,
                finding.account_id,
                finding.reference,
                finding.expected_minor,
                finding.actual_minor,
                finding.currency,
                finding.detail,
            )
            .execute(&self.pool)
            .await?;
        }

        let summary = json!({
            "total": findings.len(),
            "balance_drift": findings.iter().filter(|f| f.kind == FindingKind::BalanceDrift).count(),
            "missing_in_ledger": findings.iter().filter(|f| f.kind == FindingKind::MissingInLedger).count(),
            "missing_at_provider": findings.iter().filter(|f| f.kind == FindingKind::MissingAtProvider).count(),
            "amount_mismatch": findings.iter().filter(|f| f.kind == FindingKind::AmountMismatch).count(),
            "stale_suspense": findings.iter().filter(|f| f.kind == FindingKind::StaleSuspense).count(),
        });

        sqlx::query!(
            r#"
            UPDATE reconciliation_runs
            SET status = 'COMPLETED', finished_at = now(), summary = $2
            WHERE id = $1
            "#,
            run_id,
            summary,
        )
        .execute(&self.pool)
        .await?;

        Ok(())
    }
}

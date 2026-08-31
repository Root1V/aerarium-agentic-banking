/**
 * Registro de auditoría del personal interno.
 *
 * Toda acción de la consola queda aquí: quién, qué, sobre qué y desde dónde.
 * No es burocracia — es la única forma de investigar un incidente interno, y lo
 * primero que pide un supervisor cuando una diferencia se cerró sola.
 *
 * La tabla es de solo inserción, como el ledger: un rastro que se puede editar
 * no es un rastro.
 */

import type { Pool } from 'pg';

export const AUDIT_SCHEMA = `
CREATE SCHEMA IF NOT EXISTS backoffice;

CREATE TABLE IF NOT EXISTS backoffice.audit_log (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    operator_id  TEXT NOT NULL,
    operator_email TEXT NOT NULL,
    action       TEXT NOT NULL,
    target       TEXT,
    -- Resultado: si la acción fue denegada también se registra. Un intento
    -- rechazado es justo lo que interesa detectar.
    outcome      TEXT NOT NULL,
    detail       TEXT,
    source_ip    TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_operator ON backoffice.audit_log (operator_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_action ON backoffice.audit_log (action, created_at DESC);

CREATE OR REPLACE FUNCTION backoffice.forbid_audit_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit log is append-only: % is forbidden', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_append_only ON backoffice.audit_log;
CREATE TRIGGER trg_audit_append_only
    BEFORE UPDATE OR DELETE ON backoffice.audit_log
    FOR EACH ROW EXECUTE FUNCTION backoffice.forbid_audit_mutation();
`;

export type Outcome = 'allowed' | 'denied' | 'failed';

export interface AuditEntry {
  operatorId: string;
  operatorEmail: string;
  action: string;
  target?: string;
  outcome: Outcome;
  detail?: string;
  sourceIp?: string;
}

export interface AuditRecord extends AuditEntry {
  id: string;
  createdAt: Date;
}

export class AuditLog {
  // Campo explícito en vez de "parameter property": Node ejecuta TypeScript
  // eliminando tipos, sin transformar sintaxis que genere código.
  readonly #pool: Pool;

  constructor(pool: Pool) {
    this.#pool = pool;
  }

  async migrate(): Promise<void> {
    await this.#pool.query(AUDIT_SCHEMA);
  }

  async record(entry: AuditEntry): Promise<void> {
    await this.#pool.query(
      `INSERT INTO backoffice.audit_log
         (operator_id, operator_email, action, target, outcome, detail, source_ip)
       VALUES ($1,$2,$3,$4,$5,$6,$7)`,
      [
        entry.operatorId,
        entry.operatorEmail,
        entry.action,
        entry.target ?? null,
        entry.outcome,
        entry.detail ?? null,
        entry.sourceIp ?? null,
      ],
    );
  }

  /** Acciones recientes, para revisión. */
  async recent(limit = 50): Promise<AuditRecord[]> {
    const { rows } = await this.#pool.query(
      `SELECT id, operator_id, operator_email, action, target, outcome, detail,
              source_ip, created_at
       FROM backoffice.audit_log ORDER BY id DESC LIMIT $1`,
      [limit],
    );
    return rows.map((r) => ({
      id: String(r.id),
      operatorId: r.operator_id,
      operatorEmail: r.operator_email,
      action: r.action,
      target: r.target ?? undefined,
      outcome: r.outcome,
      detail: r.detail ?? undefined,
      sourceIp: r.source_ip ?? undefined,
      createdAt: r.created_at,
    }));
  }

  /** Acciones de una persona concreta: el punto de partida de una investigación. */
  async byOperator(operatorId: string, limit = 50): Promise<AuditRecord[]> {
    const { rows } = await this.#pool.query(
      `SELECT id, operator_id, operator_email, action, target, outcome, detail,
              source_ip, created_at
       FROM backoffice.audit_log WHERE operator_id = $1 ORDER BY id DESC LIMIT $2`,
      [operatorId, limit],
    );
    return rows.map((r) => ({
      id: String(r.id),
      operatorId: r.operator_id,
      operatorEmail: r.operator_email,
      action: r.action,
      target: r.target ?? undefined,
      outcome: r.outcome,
      detail: r.detail ?? undefined,
      sourceIp: r.source_ip ?? undefined,
      createdAt: r.created_at,
    }));
  }
}

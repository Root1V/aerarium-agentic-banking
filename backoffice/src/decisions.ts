/**
 * Registro de decisiones automáticas y su revisión humana (F3-07).
 *
 * El banco decide solo muchas cosas que afectan a una persona: si le da una
 * tarjeta, si se la bloquea por mora, si abre su cuenta, si deja pasar un pago o
 * pide confirmarlo. Cada servicio ya guarda su decisión con la evidencia, donde
 * la toma: crédito su expediente, el core cada bloqueo, el alta cada paso de KYC,
 * la capa de control cada confirmación y cada negativa. Aquí se leen todas en un
 * solo formato, sin copiarlas: la constancia es la del servicio que decidió.
 *
 * La consola solo LEE esas tablas. Lo único que escribe es la revisión: una
 * persona mira una decisión de las que más pesan —un crédito negado, una tarjeta
 * bloqueada— y deja dicho si la sostiene o no, y por qué. Es de solo agregar,
 * como todo lo que sirve de prueba.
 */

import type { Pool } from 'pg';
import { formatMicros } from './core-client.ts';

export const DECISIONS_SCHEMA = `
-- Una consola que arranca a la vez que otra (o dos suites de tests) no puede
-- crear el esquema dos veces: el candado dura lo que la migración, que va en
-- una sola transacción implícita.
SELECT pg_advisory_xact_lock(hashtext('backoffice.schema'));
CREATE SCHEMA IF NOT EXISTS backoffice;

CREATE TABLE IF NOT EXISTS backoffice.decision_reviews (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source          TEXT NOT NULL CHECK (source IN ('credit', 'mora')),
    decision_id     TEXT NOT NULL,
    customer_id     UUID NOT NULL,
    -- upheld: la decisión estuvo bien. overturned: no debió tomarse así.
    verdict         TEXT NOT NULL CHECK (verdict IN ('upheld', 'overturned')),
    -- Sin explicación una revisión no sirve: es lo que se lee después.
    note            TEXT NOT NULL CHECK (length(btrim(note)) > 0),
    operator_id     TEXT NOT NULL,
    operator_email  TEXT NOT NULL,
    reviewed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Una revisión por decisión: la segunda opinión sobre una revisión es otra
    -- cosa, y no se resuelve pisando la primera.
    UNIQUE (source, decision_id)
);

CREATE OR REPLACE FUNCTION backoffice.forbid_review_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'decision reviews are append-only: % is forbidden', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_reviews_append_only ON backoffice.decision_reviews;
CREATE TRIGGER trg_reviews_append_only
    BEFORE UPDATE OR DELETE ON backoffice.decision_reviews
    FOR EACH ROW EXECUTE FUNCTION backoffice.forbid_review_mutation();
`;

/** De dónde viene la decisión: qué servicio la tomó. */
export type DecisionSource = 'credit' | 'mora' | 'kyc' | 'control';

export const DECISION_SOURCES: readonly DecisionSource[] = ['credit', 'mora', 'kyc', 'control'];

/** Las que pasan por la cola de revisión: las que más pesan sobre una persona. */
export type ReviewableSource = 'credit' | 'mora';

export type Verdict = 'upheld' | 'overturned';

export interface Review {
  verdict: Verdict;
  note: string;
  operatorEmail: string;
  reviewedAt: Date;
}

export interface Decision {
  source: DecisionSource;
  id: string;
  customerId: string;
  decidedAt: Date;
  /** El resultado tal como lo guarda el servicio (`declined`, `BLOCKED`, `deny`…). */
  outcome: string;
  /** En una línea, para quien opera: qué se decidió y por qué. */
  summary: string;
  /** Va a la cola: un crédito negado o una tarjeta bloqueada. */
  reviewable: boolean;
  review?: Review;
}

export interface DecisionDetail extends Decision {
  /** Con qué se decidió, tal cual lo guardó el servicio (sin datos de más). */
  evidence: Record<string, unknown>;
}

export interface DecisionFilter {
  source?: DecisionSource;
  customerId?: string;
  /** Solo las que esperan revisión. */
  pending?: boolean;
  limit?: number;
}

export class DecisionNotFoundError extends Error {
  constructor(source: string, id: string) {
    super(`no hay decisión ${source}/${id}`);
    this.name = 'DecisionNotFoundError';
  }
}

export class NotReviewableError extends Error {
  constructor() {
    super('esta decisión no pasa por revisión');
    this.name = 'NotReviewableError';
  }
}

export class AlreadyReviewedError extends Error {
  constructor() {
    super('la decisión ya fue revisada');
    this.name = 'AlreadyReviewedError';
  }
}

export interface NewReview {
  source: DecisionSource;
  decisionId: string;
  verdict: Verdict;
  note: string;
  operatorId: string;
  operatorEmail: string;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

// Cada fuente, leída donde la escribe su servicio. `$1` es el cliente (o NULL),
// `$2` el id de la decisión (o NULL), `$3` solo pendientes, `$4` el límite. Las
// columnas salen con los mismos nombres para mapearlas igual.
const REVIEW_JOIN = (source: ReviewableSource, idExpr: string) => `
  LEFT JOIN backoffice.decision_reviews r ON r.source = '${source}' AND r.decision_id = ${idExpr}`;

const SOURCE_SQL: Record<DecisionSource, string> = {
  credit: `
    SELECT a.id::text AS id, a.customer_id::text AS customer_id, a.created_at AS decided_at,
           a.outcome, a.outcome IN ('declined', 'needs_more_history') AS reviewable,
           jsonb_build_object(
             'account_id', a.account_id, 'limit_micros', a.limit_micros, 'currency', a.currency,
             'score', a.score, 'model_version', a.model_version, 'reasons', a.reasons,
             'synthetic_history', a.synthetic_history, 'credit_line_id', a.credit_line_id,
             'record', a.record) AS evidence,
           r.verdict, r.note, r.operator_email, r.reviewed_at
      FROM credit.applications a ${REVIEW_JOIN('credit', 'a.id::text')}
     WHERE ($1::uuid IS NULL OR a.customer_id = $1::uuid)
       AND ($2::text IS NULL OR a.id::text = $2::text)
       AND (NOT $3::bool OR (a.outcome IN ('declined', 'needs_more_history') AND r.id IS NULL))
     ORDER BY a.created_at DESC LIMIT $4`,
  mora: `
    SELECT e.id::text AS id, e.customer_id::text AS customer_id, e.created_at AS decided_at,
           e.to_status::text AS outcome, e.to_status = 'BLOCKED' AS reviewable,
           jsonb_build_object(
             'line_id', e.line_id, 'from_status', e.from_status::text, 'reason', e.reason,
             'statement_id', e.statement_id, 'due_date', e.due_date, 'currency', e.currency,
             'minimum_micros', e.minimum_micros, 'paid_micros', e.paid_micros,
             'as_of', e.as_of) AS evidence,
           r.verdict, r.note, r.operator_email, r.reviewed_at
      FROM public.credit_line_events e ${REVIEW_JOIN('mora', 'e.id::text')}
     WHERE ($1::uuid IS NULL OR e.customer_id = $1::uuid)
       AND ($2::text IS NULL OR e.id::text = $2::text)
       AND (NOT $3::bool OR (e.to_status = 'BLOCKED' AND r.id IS NULL))
     ORDER BY e.created_at DESC LIMIT $4`,
  // El alta guarda el documento; aquí solo hacen falta sus tres últimos dígitos.
  kyc: `
    SELECT a.id::text AS id, a.customer_id::text AS customer_id, a.updated_at AS decided_at,
           a.status AS outcome, false AS reviewable,
           jsonb_build_object(
             'product_code', a.product_code, 'decision_reason', a.decision_reason,
             'document', a.document_type || ' ···' || right(a.document_number, 3),
             'country_code', a.country_code,
             'steps', COALESCE((SELECT jsonb_agg(jsonb_build_object(
                         'step', s.step, 'outcome', s.outcome, 'provider', s.provider,
                         'detail', s.detail, 'completed_at', s.completed_at) ORDER BY s.id)
                       FROM onboarding.application_steps s WHERE s.application_id = a.id), '[]'))
             AS evidence,
           NULL::text AS verdict, NULL::text AS note, NULL::text AS operator_email,
           NULL::timestamptz AS reviewed_at
      FROM onboarding.applications a
     WHERE a.status IN ('approved', 'rejected', 'manual_review')
       AND ($1::uuid IS NULL OR a.customer_id = $1::uuid)
       AND ($2::text IS NULL OR a.id::text = $2::text)
       AND NOT $3::bool
     ORDER BY a.updated_at DESC LIMIT $4`,
  // De la capa de control, lo que no es la persona moviendo su dinero sin más:
  // lo que pidió confirmación o se negó, y todo lo que hizo un agente o una regla.
  control: `
    SELECT d.id::text AS id, d.customer_id::text AS customer_id, d.decided_at,
           d.outcome, false AS reviewable,
           jsonb_build_object(
             'actor_kind', d.actor_kind, 'actor_id', d.actor_id, 'action', d.action,
             'level', d.level, 'reason', d.reason, 'operation', d.operation,
             'evidence', d.evidence) AS evidence,
           NULL::text AS verdict, NULL::text AS note, NULL::text AS operator_email,
           NULL::timestamptz AS reviewed_at
      FROM control.decisions d
     WHERE (d.outcome <> 'allow' OR d.actor_kind <> 'holder')
       AND ($1::uuid IS NULL OR d.customer_id = $1::uuid)
       AND ($2::text IS NULL OR d.id::text = $2::text)
       AND NOT $3::bool
     ORDER BY d.decided_at DESC LIMIT $4`,
};

interface Row {
  id: string;
  customer_id: string;
  decided_at: Date;
  outcome: string;
  reviewable: boolean;
  evidence: Record<string, unknown>;
  verdict: Verdict | null;
  note: string | null;
  operator_email: string | null;
  reviewed_at: Date | null;
}

export class DecisionLog {
  readonly #pool: Pool;

  constructor(pool: Pool) {
    this.#pool = pool;
  }

  async migrate(): Promise<void> {
    await this.#pool.query(DECISIONS_SCHEMA);
  }

  /** Las decisiones más recientes de todas las fuentes, o de una. */
  async list(filter: DecisionFilter = {}): Promise<Decision[]> {
    const limit = Math.min(Math.max(filter.limit ?? 100, 1), 500);
    const sources = filter.source ? [filter.source] : DECISION_SOURCES;
    const all = await Promise.all(
      sources.map((source) => this.#query(source, filter.customerId, undefined, filter.pending ?? false, limit)),
    );
    return all
      .flat()
      .sort((a, b) => b.decidedAt.getTime() - a.decidedAt.getTime())
      .slice(0, limit)
      .map(({ evidence: _evidence, ...decision }) => decision);
  }

  /** Una decisión con su evidencia. */
  async get(source: DecisionSource, id: string): Promise<DecisionDetail> {
    const [decision] = await this.#query(source, undefined, id, false, 1);
    if (!decision) throw new DecisionNotFoundError(source, id);
    return decision;
  }

  /** Deja la revisión de una persona. Una por decisión, y solo de las revisables. */
  async review(input: NewReview): Promise<DecisionDetail> {
    const decision = await this.get(input.source, input.decisionId);
    if (!decision.reviewable) throw new NotReviewableError();
    if (decision.review) throw new AlreadyReviewedError();
    try {
      await this.#pool.query(
        `INSERT INTO backoffice.decision_reviews
           (source, decision_id, customer_id, verdict, note, operator_id, operator_email)
         VALUES ($1, $2, $3, $4, $5, $6, $7)`,
        [
          input.source,
          input.decisionId,
          decision.customerId,
          input.verdict,
          input.note.trim(),
          input.operatorId,
          input.operatorEmail,
        ],
      );
    } catch (error) {
      // Dos personas revisando a la vez: gana la primera.
      if ((error as { code?: string }).code === '23505') throw new AlreadyReviewedError();
      throw error;
    }
    return this.get(input.source, input.decisionId);
  }

  async #query(
    source: DecisionSource,
    customerId: string | undefined,
    id: string | undefined,
    pending: boolean,
    limit: number,
  ): Promise<DecisionDetail[]> {
    // Un id que no es UUID no encuentra nada; mejor no llevarlo a un cast que falla.
    if (customerId !== undefined && !UUID.test(customerId)) return [];
    let rows: Row[];
    try {
      ({ rows } = await this.#pool.query<Row>(SOURCE_SQL[source], [customerId ?? null, id ?? null, pending, limit]));
    } catch (error) {
      // Un servicio que todavía no migró su esquema no deja sin registro al resto.
      if ((error as { code?: string }).code === '42P01') return [];
      throw error;
    }
    return rows.map((row) => toDecision(source, row));
  }
}

function toDecision(source: DecisionSource, row: Row): DecisionDetail {
  return {
    source,
    id: row.id,
    customerId: row.customer_id,
    decidedAt: row.decided_at,
    outcome: row.outcome,
    summary: summarize(source, row.outcome, row.evidence),
    reviewable: row.reviewable,
    evidence: row.evidence,
    ...(row.verdict
      ? {
          review: {
            verdict: row.verdict,
            note: row.note ?? '',
            operatorEmail: row.operator_email ?? '',
            reviewedAt: row.reviewed_at!,
          },
        }
      : {}),
  };
}

const CONTROL_REASONS: Record<string, string> = {
  new_payee: 'destinatario nuevo',
  amount: 'monto alto',
  high_impact: 'alto impacto',
  unknown_action: 'acción fuera del catálogo',
  human_only: 'solo la persona puede hacerlo',
  no_grant: 'sin permiso',
  grant_inactive: 'permiso vencido o retirado',
  out_of_scope: 'fuera de lo permitido',
  level_too_low: 'el permiso no alcanza',
  over_limit: 'pasa el tope del permiso',
};

const CONTROL_OUTCOMES: Record<string, string> = {
  allow: 'permitió',
  confirm: 'pidió confirmar',
  deny: 'negó',
  confirmed: 'confirmado',
  rejected: 'no se confirmó',
};

const ACTORS: Record<string, string> = {
  holder: 'la persona',
  agent: 'un agente',
  rule: 'una regla',
  assistant: 'el asistente',
};

/** La decisión en una línea, en castellano y con montos. */
export function summarize(source: DecisionSource, outcome: string, e: Record<string, unknown>): string {
  const money = (micros: unknown, currency: unknown) =>
    micros === null || micros === undefined ? '—' : formatMicros(BigInt(String(micros)), String(currency ?? ''));
  switch (source) {
    case 'credit': {
      const reasons = Array.isArray(e.reasons) ? (e.reasons as string[]) : [];
      const why = reasons.length > 0 ? `: ${reasons.join('; ')}` : '';
      const synthetic = e.synthetic_history ? ' (historial sintético)' : '';
      if (outcome === 'approved') return `Crédito aprobado con límite de ${money(e.limit_micros, e.currency)}${synthetic}`;
      if (outcome === 'needs_more_history') return `Crédito no otorgado por falta de historial${why}${synthetic}`;
      return `Crédito negado${why}${synthetic}`;
    }
    case 'mora':
      if (outcome === 'BLOCKED') {
        return `Tarjeta bloqueada por mora: pagó ${money(e.paid_micros, e.currency)} de un mínimo de ${money(
          e.minimum_micros,
          e.currency,
        )}, vencido el ${e.due_date ?? '—'}`;
      }
      return 'Tarjeta desbloqueada: se puso al día';
    case 'kyc': {
      const why = e.decision_reason ? `: ${e.decision_reason}` : '';
      if (outcome === 'approved') return 'Alta aprobada';
      if (outcome === 'rejected') return `Alta rechazada${why}`;
      return `Alta en revisión de cumplimiento${why}`;
    }
    case 'control': {
      const reason = CONTROL_REASONS[String(e.reason ?? '')];
      const actor = ACTORS[String(e.actor_kind)] ?? String(e.actor_kind);
      return `${e.action}, de ${actor}: ${CONTROL_OUTCOMES[outcome] ?? outcome}${reason ? ` (${reason})` : ''}`;
    }
  }
}

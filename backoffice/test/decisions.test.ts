/**
 * Tests del registro de decisiones automáticas y su revisión (F3-07).
 *
 * Contra la base de verdad: las decisiones se leen de las tablas de cada
 * servicio, así que aquí se siembran como las escriben ellos. Necesita los
 * esquemas del core, crédito, alta y control ya migrados (los de los tests de
 * Go y Rust).
 */

import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { after, before, describe, it } from 'node:test';
import type { AddressInfo } from 'node:net';
import pg from 'pg';

import { AuditLog } from '../src/audit.ts';
import { DecisionLog } from '../src/decisions.ts';
import { createServer } from '../src/server.ts';
import { SimAuthenticator } from '../src/sim/authenticator.ts';
import type { CoreClient } from '../src/core-client.ts';

const DATABASE_URL =
  process.env.DATABASE_URL ?? 'postgres://aibank:aibank_dev@localhost:5434/aibank';

const noCore: CoreClient = {
  listOpenFindings: async () => [],
  resolveFinding: async () => {},
  runInternalCheck: async () => ({ runId: 'r', findingsCount: 0 }),
  close: () => {},
};

let pool: pg.Pool;
let audit: AuditLog;
let server: ReturnType<typeof createServer>;
let baseUrl: string;
let available = true;

let reviewer: string;
let auditor: string;
let analyst: string;

// Un cliente por corrida: la base es compartida con otros tests.
const customer = randomUUID();
const declined = randomUUID();
const approved = randomUUID();
let blockedEvent = '';
let unblockedEvent = '';

before(async () => {
  pool = new pg.Pool({ connectionString: DATABASE_URL, max: 4 });
  try {
    await pool.query('SELECT 1 FROM credit_line_events, credit.applications, control.decisions LIMIT 1');
    await pool.query('SELECT 1 FROM onboarding.applications LIMIT 1');
  } catch {
    available = false;
    return;
  }
  audit = new AuditLog(pool);
  await audit.migrate();
  const decisions = new DecisionLog(pool);
  await decisions.migrate();
  await seed();

  const auth = new SimAuthenticator();
  reviewer = auth.issue('revision@aibank.local', ['decision_reviewer']);
  auditor = auth.issue('auditor@aibank.local', ['auditor']);
  analyst = auth.issue('analista@aibank.local', ['reconciliation_analyst']);
  server = createServer({ core: noCore, audit, decisions, authenticator: auth });
  await new Promise<void>((resolve) => server.listen(0, resolve));
  baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

after(async () => {
  if (!available) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  await pool.end();
});

/** Las decisiones de un cliente, como las dejan sus servicios. */
async function seed(): Promise<void> {
  const reasons = (list: string[]) => JSON.stringify(list);
  await pool.query(
    `INSERT INTO credit.applications
       (id, customer_id, account_id, outcome, limit_micros, currency, score, model_version, reasons, record)
     VALUES ($1, $3, 'acc', 'declined', 0, 'PEN', 410, 'v1', $4, '{"score":410}'),
            ($2, $3, 'acc', 'approved', 1500000000, 'PEN', 720, 'v1', '[]', '{"score":720}')`,
    [declined, approved, customer, reasons(['ingresos irregulares', '<script>alert(1)</script>'])],
  );
  await pool.query(`UPDATE credit.applications SET created_at = now() - interval '1 minute' WHERE id = $1`, [approved]);

  // Una tarjeta del core con su bloqueo y su desbloqueo.
  const { rows: [product] } = await pool.query(
    `INSERT INTO products (code, name, kind, currency) VALUES ($1, 'Tarjeta', 'CREDIT_CARD', 'PEN') RETURNING id`,
    [`TEST-CARD-${randomUUID()}`],
  );
  const { rows: [account] } = await pool.query(
    `INSERT INTO accounts (code, name, type, owner, owner_id, currency, product_id)
     VALUES ($1, 'Tarjeta', 'ASSET', 'CUSTOMER', $2, 'PEN', $3) RETURNING id`,
    [`TEST-CARD-${randomUUID()}`, customer, product.id],
  );
  const { rows: [line] } = await pool.query(
    `INSERT INTO credit_lines (account_id, customer_id, limit_micros, annual_rate_bps, cycle_day, payment_days, decision_ref)
     VALUES ($1, $2, 1500000000, 4500, 5, 20, $3) RETURNING id`,
    [account.id, customer, approved],
  );
  const { rows: events } = await pool.query(
    `INSERT INTO credit_line_events
       (line_id, customer_id, from_status, to_status, reason, due_date, currency, minimum_micros, paid_micros, as_of)
     VALUES ($1, $2, 'ACTIVE', 'BLOCKED', 'minimum_not_paid', '2026-11-25', 'PEN', 15000000, 1000000, now()),
            ($1, $2, 'BLOCKED', 'ACTIVE', 'caught_up', '2026-11-25', 'PEN', 15000000, 16000000, now())
     RETURNING id`,
    [line.id, customer],
  );
  blockedEvent = String(events[0].id);
  unblockedEvent = String(events[1].id);

  const application = randomUUID();
  await pool.query(
    `INSERT INTO onboarding.applications
       (id, external_ref, customer_id, product_code, status, full_name, document_type, document_number,
        date_of_birth, country_code, decision_reason)
     VALUES ($1, $2, $3, 'BASICA-PEN', 'rejected', 'Ana Pérez', 'DNI', '45678912', '1990-01-01', 'PE',
             'verificación de identidad no superada')`,
    [application, `test-${application}`, customer],
  );
  await pool.query(
    `INSERT INTO onboarding.application_steps (application_id, step, outcome, provider, detail)
     VALUES ($1, 'identity_verification', 'failed', 'sim-identity', 'la prueba de vida no coincide')`,
    [application],
  );

  await pool.query(
    `INSERT INTO control.decisions (customer_id, actor_kind, actor_id, action, level, outcome, reason, evidence)
     VALUES ($1, 'agent', 'agt-1', 'mandate_payment', 3, 'deny', 'over_limit', 'grant:mandate:m-1'),
            ($1, 'holder', '', 'transfer', 2, 'allow', '', '')`,
    [customer],
  );
}

function call(path: string, token?: string, body?: unknown): Promise<Response> {
  return fetch(`${baseUrl}${path}`, {
    method: body === undefined ? 'GET' : 'POST',
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

describe('el registro', () => {
  it('reúne las decisiones de cada servicio, dichas en una línea', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const html = await (await call(`/decisions?customer=${customer}`, reviewer)).text();

    assert.match(html, /Crédito negado: ingresos irregulares/);
    assert.match(html, /Crédito aprobado con límite de 1500,00 PEN/);
    assert.match(html, /Tarjeta bloqueada por mora: pagó 1,00 PEN de un mínimo de 15,00 PEN, vencido el 2026-11-25/);
    assert.match(html, /Tarjeta desbloqueada: se puso al día/);
    assert.match(html, /Alta rechazada: verificación de identidad no superada/);
    assert.match(html, /mandate_payment, de un agente: negó \(pasa el tope del permiso\)/);
    assert.doesNotMatch(html, /transfer, de la persona/, 'la persona moviendo su dinero sin más no es una decisión que revisar');
  });

  it('el motivo que viene del motor no se inyecta en la página', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const html = await (await call(`/decisions?customer=${customer}`, reviewer)).text();
    assert.doesNotMatch(html, /<script>alert/);
    assert.match(html, /&lt;script&gt;alert/);
  });

  it('el detalle trae la evidencia, sin el documento completo', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const credit = (await (await call(`/decisions/credit/${declined}`, reviewer)).json()) as {
      evidence: { score: number; reasons: string[] };
    };
    assert.equal(credit.evidence.score, 410);
    assert.deepEqual(credit.evidence.reasons, ['ingresos irregulares', '<script>alert(1)</script>']);

    const list = (await (await call(`/decisions?customer=${customer}&source=kyc`, reviewer)).text());
    const id = /kyc\/([0-9a-f-]{36})/.exec(list)?.[1];
    assert.ok(id);
    const kyc = await (await call(`/decisions/kyc/${id}`, reviewer)).text();
    assert.match(kyc, /DNI ···912/);
    assert.doesNotMatch(kyc, /45678912/, 'el documento completo no hace falta para revisar');
    assert.match(kyc, /la prueba de vida no coincide/);
  });

  it('una fuente que no existe es un 400, y una decisión que no existe un 404', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    assert.equal((await call('/decisions?source=otra', reviewer)).status, 400);
    assert.equal((await call(`/decisions/credit/${randomUUID()}`, reviewer)).status, 404);
  });
});

describe('quién las ve', () => {
  it('el analista de conciliación no ve decisiones de clientes, y el intento queda', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    assert.equal((await call(`/decisions?customer=${customer}`, analyst)).status, 403);
    const denied = (await audit.recent(200)).find(
      (e) => e.action === 'decisions:list' && e.outcome === 'denied' && e.target === `customer=${customer}`,
    );
    assert.equal(denied?.operatorEmail, 'analista@aibank.local');
  });

  it('el auditor las ve pero no las revisa', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const html = await (await call(`/decisions/queue?customer=${customer}`, auditor)).text();
    assert.match(html, /Solo lectura/);
    const response = await call('/decisions/review', auditor, {
      source: 'credit',
      decision_id: declined,
      verdict: 'upheld',
      note: 'intento',
    });
    assert.equal(response.status, 403);
  });

  it('mirar las decisiones de un cliente queda en el rastro', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    await call(`/decisions/credit/${declined}`, reviewer);
    const read = (await audit.recent(200)).find(
      (e) => e.action === 'decisions:read' && e.target === `credit/${declined}` && e.outcome === 'allowed',
    );
    assert.equal(read?.operatorEmail, 'revision@aibank.local');
  });
});

describe('la cola de revisión', () => {
  it('trae los créditos negados y los bloqueos, no lo aprobado ni los desbloqueos', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const html = await (await call(`/decisions/queue?customer=${customer}`, reviewer)).text();
    assert.match(html, new RegExp(`credit/${declined}`));
    assert.match(html, new RegExp(`mora/${blockedEvent}<`));
    assert.doesNotMatch(html, new RegExp(`credit/${approved}`));
    assert.doesNotMatch(html, new RegExp(`mora/${unblockedEvent}<`));
    assert.doesNotMatch(html, /Alta rechazada/, 'el alta la revisa cumplimiento, en su propio flujo');
  });

  it('revisar exige decir por qué', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/decisions/review', reviewer, {
      source: 'mora',
      decision_id: blockedEvent,
      verdict: 'upheld',
      note: '  ',
    });
    assert.equal(response.status, 400);
    assert.equal(((await response.json()) as { error: string }).error, 'note_required');
  });

  it('lo que no pasa por la cola no se revisa aquí', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/decisions/review', reviewer, {
      source: 'credit',
      decision_id: approved,
      verdict: 'upheld',
      note: 'bien aprobado',
    });
    assert.equal(response.status, 409);
    assert.equal(((await response.json()) as { error: string }).error, 'not_reviewable');
  });

  it('una revisión sale de la cola, queda en el registro y en el rastro, y no se repite', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/decisions/review', reviewer, {
      source: 'mora',
      decision_id: blockedEvent,
      verdict: 'overturned',
      note: 'pagó por ventanilla el día del vencimiento; ticket OPS-77',
    });
    assert.equal(response.status, 200);
    const reviewed = (await response.json()) as { review: { verdict: string; operatorEmail: string } };
    assert.equal(reviewed.review.verdict, 'overturned');
    assert.equal(reviewed.review.operatorEmail, 'revision@aibank.local');

    const queue = await (await call(`/decisions/queue?customer=${customer}`, reviewer)).text();
    assert.doesNotMatch(queue, new RegExp(`mora/${blockedEvent}<`));
    const list = await (await call(`/decisions?customer=${customer}`, reviewer)).text();
    assert.match(list, /No debió tomarse así · revision@aibank.local<br>pagó por ventanilla/);

    const entry = (await audit.recent(200)).find(
      (e) => e.action === 'decisions:review' && e.target === `mora/${blockedEvent}` && e.outcome === 'allowed',
    );
    assert.match(entry?.detail ?? '', /^overturned: .*OPS-77/);

    const again = await call('/decisions/review', reviewer, {
      source: 'mora',
      decision_id: blockedEvent,
      verdict: 'upheld',
      note: 'cambio de opinión',
    });
    assert.equal(again.status, 409);
    assert.equal(((await again.json()) as { error: string }).error, 'already_reviewed');
  });

  it('la revisión es de solo agregar', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    await assert.rejects(
      () => pool.query(`UPDATE backoffice.decision_reviews SET verdict = 'upheld' WHERE decision_id = $1`, [blockedEvent]),
      /append-only/,
    );
  });
});

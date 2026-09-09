/**
 * Tests de la consola de operaciones.
 *
 * El grupo que más importa es el de permisos y auditoría: el fraude interno es
 * un vector real, y una consola que permite actuar sin dejar rastro atribuido a
 * una persona es indefendible ante un supervisor.
 *
 * Requiere PostgreSQL: docker compose -f platform/docker-compose.yml up -d
 */

import assert from 'node:assert/strict';
import { after, before, describe, it } from 'node:test';
import type { AddressInfo } from 'node:net';
import pg from 'pg';

import { AuditLog } from '../src/audit.ts';
import { createServer } from '../src/server.ts';
import { SimAuthenticator } from '../src/sim/authenticator.ts';
import { formatMicros, type CoreClient, type Finding } from '../src/core-client.ts';
import { CoreUnavailableError, FindingNotFoundError } from '../src/core-client.ts';
import { escapeHtml } from '../src/views.ts';

const DATABASE_URL =
  process.env.DATABASE_URL ?? 'postgres://aibank:aibank_dev@localhost:5434/aibank';

/** Core falso: los tests del core real ya cubren su comportamiento. */
class FakeCore implements CoreClient {
  resolved: Array<{ id: string; resolution: string }> = [];
  unavailable = false;
  findings: Finding[] = [
    {
      id: '1',
      kind: 'FINDING_KIND_BALANCE_DRIFT',
      accountId: 'acc-1',
      reference: 'ref-1',
      expectedMicros: 200000000n,
      actualMicros: 250000000n,
      currency: 'USD',
      detail: 'saldo materializado 25000 contra 20000 de los asientos',
    },
  ];

  async listOpenFindings(): Promise<Finding[]> {
    if (this.unavailable) throw new CoreUnavailableError('caído');
    return this.findings;
  }

  async resolveFinding(findingId: string, resolution: string): Promise<void> {
    if (this.unavailable) throw new CoreUnavailableError('caído');
    if (!this.findings.some((f) => f.id === findingId)) {
      throw new FindingNotFoundError(findingId);
    }
    this.resolved.push({ id: findingId, resolution });
  }

  async runInternalCheck() {
    if (this.unavailable) throw new CoreUnavailableError('caído');
    return { runId: 'run-1', findingsCount: this.findings.length };
  }

  close(): void {}
}

let pool: pg.Pool;
let audit: AuditLog;
let core: FakeCore;
let auth: SimAuthenticator;
let baseUrl: string;
let server: ReturnType<typeof createServer>;
let available = true;

let analystToken: string;
let viewerToken: string;
let auditorToken: string;

before(async () => {
  pool = new pg.Pool({ connectionString: DATABASE_URL, max: 4 });
  try {
    await pool.query('SELECT 1');
  } catch {
    available = false;
    return;
  }

  audit = new AuditLog(pool);
  await audit.migrate();

  core = new FakeCore();
  auth = new SimAuthenticator();
  analystToken = auth.issue('analista@aibank.local', ['reconciliation_analyst']);
  viewerToken = auth.issue('soporte@aibank.local', ['viewer']);
  auditorToken = auth.issue('auditor@aibank.local', ['auditor']);

  server = createServer({ core, audit, authenticator: auth });
  await new Promise<void>((resolve) => server.listen(0, resolve));
  baseUrl = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

after(async () => {
  if (!available) return;
  await new Promise<void>((resolve) => server.close(() => resolve()));
  await pool.end();
});

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

async function auditEntriesFor(action: string) {
  const entries = await audit.recent(200);
  return entries.filter((e) => e.action === action);
}

describe('autenticación', () => {
  it('sin token se rechaza', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings');
    assert.equal(response.status, 401);
  });

  it('con token inválido se rechaza', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings', 'token-inventado');
    assert.equal(response.status, 401);
  });

  it('/health no exige autenticación', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/health');
    assert.equal(response.status, 200);
  });
});

describe('permisos', () => {
  it('un perfil de solo lectura ve la cola', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings', viewerToken);
    assert.equal(response.status, 200);
    const html = await response.text();
    assert.match(html, /Saldo descuadrado/);
  });

  // Quien solo consulta no puede cerrar una diferencia.
  it('un perfil de solo lectura NO puede resolver', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const before = core.resolved.length;

    const response = await call('/findings/resolve', viewerToken, {
      finding_id: '1',
      resolution: 'intento indebido',
    });

    assert.equal(response.status, 403);
    assert.equal(core.resolved.length, before, 'no debe llegar al core');
  });

  // Quien audita no debe poder alterar lo que audita.
  it('un auditor tampoco puede resolver', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings/resolve', auditorToken, {
      finding_id: '1',
      resolution: 'intento indebido',
    });
    assert.equal(response.status, 403);
  });

  it('el analista de conciliación sí puede resolver', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings/resolve', analystToken, {
      finding_id: '1',
      resolution: 'ajuste contable aplicado, ticket OPS-1234',
    });

    assert.equal(response.status, 200);
    assert.ok(
      core.resolved.some((r) => r.resolution.includes('OPS-1234')),
      'la resolución debe llegar al core',
    );
  });

  it('la vista no ofrece la acción a quien no puede ejecutarla', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const viewerHtml = await (await call('/findings', viewerToken)).text();
    const analystHtml = await (await call('/findings', analystToken)).text();

    assert.match(viewerHtml, /Solo lectura/);
    assert.doesNotMatch(analystHtml, /Solo lectura/);
  });
});

describe('auditoría', () => {
  it('cada acción permitida queda atribuida a una persona', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    await call('/findings/resolve', analystToken, {
      finding_id: '1',
      resolution: 'ticket OPS-9999',
    });

    const entries = await auditEntriesFor('findings:resolve');
    const entry = entries.find((e) => e.detail?.includes('OPS-9999'));
    assert.ok(entry, 'la acción debe estar registrada');
    assert.equal(entry.operatorEmail, 'analista@aibank.local');
    assert.equal(entry.outcome, 'allowed');
    assert.equal(entry.target, '1');
    assert.ok(entry.sourceIp, 'debe registrarse el origen');
  });

  // Un intento rechazado es justo lo que interesa detectar.
  it('los intentos denegados también se registran', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    await call('/findings/resolve', viewerToken, {
      finding_id: '42',
      resolution: 'intento indebido',
    });

    const denied = (await auditEntriesFor('findings:resolve')).filter(
      (e) => e.outcome === 'denied' && e.target === '42',
    );
    assert.ok(denied.length > 0, 'la denegación debe quedar registrada');
    assert.equal(denied[0]!.operatorEmail, 'soporte@aibank.local');
  });

  it('el rastro es de solo inserción', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    await audit.record({
      operatorId: 'op-1',
      operatorEmail: 'x@aibank.local',
      action: 'test:immutability',
      outcome: 'allowed',
    });

    await assert.rejects(
      () =>
        pool.query(
          `UPDATE backoffice.audit_log SET action = 'alterada' WHERE action = 'test:immutability'`,
        ),
      /append-only/,
    );
    await assert.rejects(
      () => pool.query(`DELETE FROM backoffice.audit_log WHERE action = 'test:immutability'`),
      /append-only/,
    );
  });

  it('se puede reconstruir lo que hizo una persona', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const entries = await audit.recent(200);
    const analyst = entries.find((e) => e.operatorEmail === 'analista@aibank.local');
    assert.ok(analyst);

    const history = await audit.byOperator(analyst.operatorId, 100);
    assert.ok(history.length > 0, 'debe poder investigarse por persona');
    assert.ok(history.every((e) => e.operatorId === analyst.operatorId));
  });
});

describe('resolución', () => {
  it('exige explicar cómo se resolvió', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings/resolve', analystToken, {
      finding_id: '1',
      resolution: '   ',
    });

    assert.equal(response.status, 400);
    const body = (await response.json()) as { error?: string; message?: string };
    assert.equal(body.error, 'resolution_required');
  });

  it('un hallazgo inexistente devuelve 404', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    const response = await call('/findings/resolve', analystToken, {
      finding_id: '99999',
      resolution: 'ticket OPS-1',
    });
    assert.equal(response.status, 404);
  });

  it('si el core no responde se informa sin filtrar el detalle interno', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    core.unavailable = true;
    const response = await call('/findings', analystToken);
    core.unavailable = false;

    assert.equal(response.status, 503);
    const body = (await response.json()) as { error?: string; message?: string };
    assert.equal(body.error, 'core_unavailable');
    assert.equal(body.message, undefined, 'no debe exponerse el detalle del fallo');
  });
});

describe('presentación', () => {
  it('el dinero se formatea con aritmética entera', () => {
    assert.equal(formatMicros(250000000n, 'USD'), '250,00 USD');
    assert.equal(formatMicros(50000n, 'USD'), '0,05 USD');
    assert.equal(formatMicros(-123450000n, 'PEN'), '-123,45 PEN');
  });

  // Una diferencia de conciliación por debajo del centavo tiene que verse. Si se
  // formateara a dos decimales fijos aparecería como "0,00" y el operador leería
  // la alerta como si no hubiera nada que investigar.
  it('una diferencia menor a un centavo no se muestra como cero', () => {
    assert.equal(formatMicros(3000n, 'USD'), '0,003 USD');
    assert.equal(formatMicros(1n, 'USD'), '0,000001 USD');
    assert.equal(formatMicros(10001n, 'USD'), '0,010001 USD');
  });

  // El detalle de un hallazgo viene del core y podría contener texto arbitrario.
  it('el HTML escapa el contenido', () => {
    assert.equal(
      escapeHtml('<script>alert("x")</script>'),
      '&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;',
    );
  });

  it('un detalle malicioso no se inyecta en la página', async (t) => {
    if (!available) return t.skip('PostgreSQL no disponible');
    core.findings = [
      {
        id: '7',
        kind: 'FINDING_KIND_AMOUNT_MISMATCH',
        accountId: 'acc-9',
        reference: '<img src=x onerror=alert(1)>',
        expectedMicros: 1000000n,
        actualMicros: 2000000n,
        currency: 'USD',
        detail: 'referencia sospechosa',
      },
    ];

    const html = await (await call('/findings', viewerToken)).text();
    core.findings = [];

    assert.doesNotMatch(html, /<img src=x/);
    assert.match(html, /&lt;img src=x/);
  });
});

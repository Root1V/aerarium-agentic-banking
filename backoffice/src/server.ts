/**
 * Consola de operaciones.
 *
 * Regla que gobierna todo el módulo: **ninguna acción ocurre sin quedar
 * atribuida a una persona**, y los intentos denegados se registran igual que los
 * permitidos — un intento rechazado es justo lo que interesa detectar.
 */

import http from 'node:http';
import { AuditLog, type Outcome } from './audit.ts';
import {
  bearerToken,
  can,
  UnauthenticatedError,
  type Authenticator,
  type Operator,
  type Permission,
} from './auth.ts';
import {
  CoreUnavailableError,
  FindingNotFoundError,
  formatMicros,
  type CoreClient,
  type Finding,
} from './core-client.ts';
import { renderFindings, renderLayout } from './views.ts';

export interface ServerOptions {
  core: CoreClient;
  audit: AuditLog;
  authenticator: Authenticator;
}

interface RequestContext {
  operator: Operator;
  sourceIp: string;
}

export function createServer(options: ServerOptions): http.Server {
  return http.createServer((req, res) => {
    handle(req, res, options).catch((error) => {
      // Un fallo inesperado no debe filtrar el detalle interno al navegador.
      console.error('error no controlado', error);
      sendJson(res, 500, { error: 'internal_error' });
    });
  });
}

async function handle(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  options: ServerOptions,
): Promise<void> {
  const url = new URL(req.url ?? '/', 'http://localhost');

  if (req.method === 'GET' && url.pathname === '/health') {
    return sendJson(res, 200, { status: 'ok' });
  }

  let context: RequestContext;
  try {
    context = {
      operator: await options.authenticator.authenticate(
        bearerToken(req.headers.authorization),
      ),
      sourceIp: sourceIpOf(req),
    };
  } catch {
    return sendJson(res, 401, { error: 'unauthenticated' });
  }

  if (req.method === 'GET' && url.pathname === '/findings') {
    return listFindings(res, options, context);
  }
  if (req.method === 'POST' && url.pathname === '/findings/resolve') {
    return resolveFinding(req, res, options, context);
  }
  if (req.method === 'POST' && url.pathname === '/reconciliation/run') {
    return runCheck(res, options, context);
  }
  if (req.method === 'GET' && url.pathname === '/audit') {
    return listAudit(res, options, context);
  }

  sendJson(res, 404, { error: 'not_found' });
}

// ---------------------------------------------------------------- rutas

async function listFindings(
  res: http.ServerResponse,
  options: ServerOptions,
  context: RequestContext,
): Promise<void> {
  if (!(await authorize(options, context, 'reconciliation:read', 'findings:list', res))) {
    return;
  }

  let findings: Finding[];
  try {
    findings = await options.core.listOpenFindings();
  } catch (error) {
    await record(options, context, 'findings:list', undefined, 'failed', String(error));
    if (error instanceof CoreUnavailableError) {
      return sendJson(res, 503, { error: 'core_unavailable' });
    }
    throw error;
  }

  await record(options, context, 'findings:list', undefined, 'allowed');
  sendHtml(res, 200, renderLayout('Diferencias abiertas', renderFindings(findings, context.operator)));
}

async function resolveFinding(
  req: http.IncomingMessage,
  res: http.ServerResponse,
  options: ServerOptions,
  context: RequestContext,
): Promise<void> {
  const body = await readJson(req);
  const findingId = String(body.finding_id ?? '');
  const resolution = String(body.resolution ?? '').trim();

  // La comprobación de permiso va ANTES de tocar el core, y su denegación queda
  // registrada con el hallazgo que se intentó cerrar.
  if (
    !(await authorize(options, context, 'reconciliation:resolve', 'findings:resolve', res, findingId))
  ) {
    return;
  }

  if (!findingId) {
    return sendJson(res, 400, { error: 'invalid_request', message: 'falta finding_id' });
  }
  if (!resolution) {
    // Cerrar una diferencia sin explicar cómo la deja invisible para la próxima
    // auditoría, que es justo lo contrario de para qué existe la cola.
    return sendJson(res, 400, {
      error: 'resolution_required',
      message: 'hay que explicar cómo se resolvió',
    });
  }

  try {
    await options.core.resolveFinding(findingId, resolution);
  } catch (error) {
    await record(options, context, 'findings:resolve', findingId, 'failed', String(error));
    if (error instanceof FindingNotFoundError) {
      return sendJson(res, 404, { error: 'not_found' });
    }
    if (error instanceof CoreUnavailableError) {
      return sendJson(res, 503, { error: 'core_unavailable' });
    }
    throw error;
  }

  await record(options, context, 'findings:resolve', findingId, 'allowed', resolution);
  sendJson(res, 200, { resolved: true });
}

async function runCheck(
  res: http.ServerResponse,
  options: ServerOptions,
  context: RequestContext,
): Promise<void> {
  if (!(await authorize(options, context, 'reconciliation:run', 'reconciliation:run', res))) {
    return;
  }

  try {
    const result = await options.core.runInternalCheck();
    await record(
      options,
      context,
      'reconciliation:run',
      result.runId,
      'allowed',
      `${result.findingsCount} diferencias`,
    );
    sendJson(res, 200, { run_id: result.runId, findings_count: result.findingsCount });
  } catch (error) {
    await record(options, context, 'reconciliation:run', undefined, 'failed', String(error));
    if (error instanceof CoreUnavailableError) {
      return sendJson(res, 503, { error: 'core_unavailable' });
    }
    throw error;
  }
}

async function listAudit(
  res: http.ServerResponse,
  options: ServerOptions,
  context: RequestContext,
): Promise<void> {
  // Cualquiera con acceso de lectura puede ver el rastro: la transparencia
  // interna es parte del control.
  if (!(await authorize(options, context, 'reconciliation:read', 'audit:list', res))) {
    return;
  }
  const entries = await options.audit.recent();
  await record(options, context, 'audit:list', undefined, 'allowed');
  sendJson(res, 200, { entries });
}

// ---------------------------------------------------------------- apoyo

/** Comprueba el permiso, registra la denegación y responde 403 si no lo tiene. */
async function authorize(
  options: ServerOptions,
  context: RequestContext,
  permission: Permission,
  action: string,
  res: http.ServerResponse,
  target?: string,
): Promise<boolean> {
  if (can(context.operator, permission)) return true;

  await record(options, context, action, target, 'denied', `sin permiso ${permission}`);
  sendJson(res, 403, { error: 'forbidden' });
  return false;
}

function record(
  options: ServerOptions,
  context: RequestContext,
  action: string,
  target: string | undefined,
  outcome: Outcome,
  detail?: string,
): Promise<void> {
  return options.audit.record({
    operatorId: context.operator.id,
    operatorEmail: context.operator.email,
    action,
    target,
    outcome,
    detail,
    sourceIp: context.sourceIp,
  });
}

function sourceIpOf(req: http.IncomingMessage): string {
  const forwarded = req.headers['x-forwarded-for'];
  if (typeof forwarded === 'string' && forwarded.length > 0) {
    return forwarded.split(',')[0]!.trim();
  }
  return req.socket.remoteAddress ?? 'desconocida';
}

async function readJson(req: http.IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 64 * 1024) throw new Error('cuerpo demasiado grande');
    chunks.push(chunk as Buffer);
  }
  if (chunks.length === 0) return {};
  try {
    const parsed = JSON.parse(Buffer.concat(chunks).toString('utf8'));
    return typeof parsed === 'object' && parsed !== null ? parsed : {};
  } catch {
    return {};
  }
}

function sendJson(res: http.ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(
    JSON.stringify(body, (_key, value) =>
      typeof value === 'bigint' ? value.toString() : value,
    ),
  );
}

function sendHtml(res: http.ServerResponse, status: number, html: string): void {
  res.writeHead(status, {
    'Content-Type': 'text/html; charset=utf-8',
    // La consola no debe poder embeberse ni cargar recursos externos.
    'Content-Security-Policy': "default-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'",
    'X-Content-Type-Options': 'nosniff',
    'Referrer-Policy': 'no-referrer',
  });
  res.end(html);
}

export { formatMicros, UnauthenticatedError };

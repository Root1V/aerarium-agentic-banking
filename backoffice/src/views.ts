/**
 * Vistas de la consola.
 *
 * HTML generado en el servidor, sin framework ni empaquetador: una consola
 * interna de listas y formularios no justifica esa complejidad, y menos
 * dependencias es menos superficie que auditar en un sistema financiero.
 */

import { formatMicros, type Finding, type FindingKind } from './core-client.ts';
import { can, type Operator } from './auth.ts';
import type { Decision, DecisionSource, Verdict } from './decisions.ts';

/** Escapa texto antes de insertarlo en HTML. */
export function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

const KIND_LABELS: Record<FindingKind, string> = {
  FINDING_KIND_BALANCE_DRIFT: 'Saldo descuadrado',
  FINDING_KIND_MISSING_IN_LEDGER: 'Falta en el ledger',
  FINDING_KIND_MISSING_AT_PROVIDER: 'Falta en el proveedor',
  FINDING_KIND_AMOUNT_MISMATCH: 'Importes distintos',
  FINDING_KIND_STALE_SUSPENSE: 'Dinero detenido',
  FINDING_KIND_UNSPECIFIED: 'Sin clasificar',
};

export function renderLayout(title: string, body: string): string {
  return `<!doctype html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>${escapeHtml(title)} · AIBank</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a1a; }
  h1 { font-size: 1.4rem; }
  table { border-collapse: collapse; width: 100%; margin-top: 1rem; }
  th, td { text-align: left; padding: .5rem .75rem; border-bottom: 1px solid #e4e4e7; }
  th { font-size: .8rem; text-transform: uppercase; color: #52525b; }
  .empty { color: #52525b; padding: 2rem 0; }
  .num { font-variant-numeric: tabular-nums; text-align: right; }
  .kind { font-weight: 600; }
  .note { color: #52525b; font-size: .85rem; margin-top: 1.5rem; }
</style>
</head>
<body>
<h1>${escapeHtml(title)}</h1>
${body}
</body>
</html>`;
}

export function renderFindings(findings: readonly Finding[], operator: Operator): string {
  if (findings.length === 0) {
    return '<p class="empty">No hay diferencias abiertas.</p>';
  }

  const rows = findings
    .map(
      (f) => `<tr>
  <td>${escapeHtml(f.id)}</td>
  <td class="kind">${escapeHtml(KIND_LABELS[f.kind] ?? f.kind)}</td>
  <td>${escapeHtml(f.reference)}</td>
  <td class="num">${escapeHtml(formatMicros(f.expectedMicros, f.currency))}</td>
  <td class="num">${escapeHtml(formatMicros(f.actualMicros, f.currency))}</td>
  <td>${escapeHtml(f.detail)}</td>
</tr>`,
    )
    .join('\n');

  // Quien no puede resolver ve la cola pero no la acción: la interfaz refleja el
  // permiso real en vez de ofrecer un botón que el servidor va a rechazar.
  const footer = can(operator, 'reconciliation:resolve')
    ? '<p class="note">Para cerrar una diferencia hay que indicar cómo se resolvió.</p>'
    : '<p class="note">Solo lectura: tu perfil no puede cerrar diferencias.</p>';

  return `<table>
<thead><tr>
  <th>#</th><th>Tipo</th><th>Referencia</th>
  <th class="num">Esperado</th><th class="num">Registrado</th><th>Detalle</th>
</tr></thead>
<tbody>
${rows}
</tbody>
</table>
${footer}`;
}

const SOURCE_LABELS: Record<DecisionSource, string> = {
  credit: 'Crédito',
  mora: 'Mora',
  kyc: 'Alta (KYC)',
  control: 'Capa de control',
};

const VERDICT_LABELS: Record<Verdict, string> = {
  upheld: 'Se sostiene',
  overturned: 'No debió tomarse así',
};

/** Fecha y hora de Lima: es la hora en la que vive el cliente y opera el banco. */
function limaTime(date: Date): string {
  return date.toLocaleString('es-PE', { timeZone: 'America/Lima', dateStyle: 'short', timeStyle: 'short' });
}

export function renderDecisions(decisions: readonly Decision[], operator: Operator, queue: boolean): string {
  if (decisions.length === 0) {
    return queue
      ? '<p class="empty">No hay decisiones esperando revisión.</p>'
      : '<p class="empty">No hay decisiones automáticas con ese filtro.</p>';
  }

  const rows = decisions
    .map(
      (d) => `<tr>
  <td>${escapeHtml(limaTime(d.decidedAt))}</td>
  <td class="kind">${escapeHtml(SOURCE_LABELS[d.source])}</td>
  <td>${escapeHtml(d.customerId)}</td>
  <td>${escapeHtml(d.summary)}</td>
  <td>${
    d.review
      ? `${escapeHtml(VERDICT_LABELS[d.review.verdict])} · ${escapeHtml(d.review.operatorEmail)}<br>${escapeHtml(d.review.note)}`
      : d.reviewable
        ? 'Pendiente'
        : '—'
  }</td>
  <td>${escapeHtml(`${d.source}/${d.id}`)}</td>
</tr>`,
    )
    .join('\n');

  // Como en la conciliación: la interfaz dice lo que el perfil puede hacer.
  const footer = !can(operator, 'decisions:review')
    ? '<p class="note">Solo lectura: tu perfil no puede revisar decisiones.</p>'
    : queue
      ? '<p class="note">Para revisar hay que decir si la decisión se sostiene y por qué. ' +
        'La revisión deja constancia; revertir la decisión (aprobar a mano, levantar un bloqueo) ' +
        'no se hace desde aquí.</p>'
      : '';

  return `<table>
<thead><tr>
  <th>Cuándo</th><th>Fuente</th><th>Cliente</th><th>Decisión</th><th>Revisión</th><th>Referencia</th>
</tr></thead>
<tbody>
${rows}
</tbody>
</table>
${footer}`;
}

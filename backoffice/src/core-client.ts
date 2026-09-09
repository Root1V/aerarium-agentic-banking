/**
 * Cliente gRPC del core.
 *
 * Carga el contrato desde `contracts/proto` en tiempo de ejecución: el .proto
 * sigue siendo la única fuente de verdad, sin un paso de generación que pueda
 * quedar desactualizado respecto al servidor.
 *
 * El backoffice NO lee la base del core: eso rompería el límite entre servicios
 * y dejaría las reglas del core (qué se puede resolver, qué no) fuera de juego.
 */

import { fileURLToPath } from 'node:url';
import path from 'node:path';
import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';

const here = path.dirname(fileURLToPath(import.meta.url));
const PROTO_ROOT = path.resolve(here, '../../contracts/proto');
const PROTO_FILE = path.join(PROTO_ROOT, 'aibank/core/v1/core.proto');

export type FindingKind =
  | 'FINDING_KIND_BALANCE_DRIFT'
  | 'FINDING_KIND_MISSING_IN_LEDGER'
  | 'FINDING_KIND_MISSING_AT_PROVIDER'
  | 'FINDING_KIND_AMOUNT_MISMATCH'
  | 'FINDING_KIND_STALE_SUSPENSE'
  | 'FINDING_KIND_UNSPECIFIED';

export interface Finding {
  id: string;
  kind: FindingKind;
  accountId: string;
  reference: string;
  /** Enteros en micras (10^-6): el dinero nunca viaja como decimal. */
  expectedMicros: bigint;
  actualMicros: bigint;
  currency: string;
  detail: string;
}

export class CoreUnavailableError extends Error {
  constructor(cause: string) {
    super(`core no disponible: ${cause}`);
    this.name = 'CoreUnavailableError';
  }
}

export class FindingNotFoundError extends Error {
  constructor(id: string) {
    super(`hallazgo ${id} inexistente o ya resuelto`);
    this.name = 'FindingNotFoundError';
  }
}

export interface CoreClient {
  listOpenFindings(limit?: number): Promise<Finding[]>;
  resolveFinding(findingId: string, resolution: string): Promise<void>;
  runInternalCheck(): Promise<{ runId: string; findingsCount: number }>;
  close(): void;
}

export function connectCore(address: string): CoreClient {
  const definition = protoLoader.loadSync(PROTO_FILE, {
    includeDirs: [PROTO_ROOT],
    keepCase: false,
    longs: String,
    enums: String,
    defaults: true,
  });
  const proto = grpc.loadPackageDefinition(definition) as any;
  const service = proto.aibank.core.v1.ReconciliationService;

  // Sin TLS: el core solo escucha en la red interna; la autenticación entre
  // servicios se resuelve en la malla.
  const client = new service(address, grpc.credentials.createInsecure());

  const call = <T>(method: string, request: unknown): Promise<T> =>
    new Promise((resolve, reject) => {
      client[method](request, (err: grpc.ServiceError | null, response: T) => {
        if (!err) return resolve(response);
        if (err.code === grpc.status.NOT_FOUND) {
          return reject(new FindingNotFoundError(String((request as any).findingId)));
        }
        reject(new CoreUnavailableError(err.details || err.message));
      });
    });

  return {
    async listOpenFindings(limit = 50) {
      const response = await call<{ findings: any[] }>('listOpenFindings', { limit });
      return (response.findings ?? []).map((f) => ({
        id: String(f.id),
        kind: f.kind as FindingKind,
        accountId: f.accountId ?? '',
        reference: f.reference ?? '',
        expectedMicros: BigInt(f.expectedMicros ?? 0),
        actualMicros: BigInt(f.actualMicros ?? 0),
        currency: f.currency ?? '',
        detail: f.detail ?? '',
      }));
    },

    async resolveFinding(findingId, resolution) {
      await call('resolveFinding', { findingId, resolution });
    },

    async runInternalCheck() {
      const response = await call<{ runId: string; findingsCount: number }>(
        'runInternalCheck',
        {},
      );
      return { runId: response.runId, findingsCount: Number(response.findingsCount) };
    },

    close() {
      client.close();
    },
  };
}

/** Micras por unidad mayor: 1 USD = 1.000.000 micras. */
const MICROS_PER_UNIT = 1_000_000n;

/**
 * Formatea un importe en micras. Aritmética entera con `bigint`, sin flotantes.
 *
 * Dos decimales, salvo que el importe tenga fracción de centavo: ahí se muestran
 * los que hagan falta hasta seis. En una consola de conciliación esto no es un
 * detalle estético — una diferencia de 3.000 micras mostrada como "0,00" es una
 * alerta que se lee como si no hubiera nada que investigar.
 */
export function formatMicros(amountMicros: bigint, currency: string): string {
  const negative = amountMicros < 0n;
  const absolute = negative ? -amountMicros : amountMicros;
  const units = absolute / MICROS_PER_UNIT;
  const fraction = absolute % MICROS_PER_UNIT;

  const digits = fraction.toString().padStart(6, '0');
  let end = digits.length;
  while (end > 2 && digits[end - 1] === '0') end--;

  const sign = negative ? '-' : '';
  return `${sign}${units.toString()},${digits.slice(0, end)} ${currency}`;
}

/**
 * Arranque de la consola de operaciones.
 *
 * La autenticación real se inyecta aquí. Mientras no esté integrada con el
 * proveedor de identidad corporativo, el arranque exige que se declare
 * explícitamente el uso del sustituto de desarrollo.
 */

import pg from 'pg';
import { AuditLog } from './audit.ts';
import { connectCore } from './core-client.ts';
import { createServer } from './server.ts';
import { SimAuthenticator } from './sim/authenticator.ts';

const CORE_ADDR = process.env.CORE_ADDR ?? 'localhost:50051';
const DATABASE_URL =
  process.env.DATABASE_URL ?? 'postgres://aibank:aibank_dev@localhost:5434/aibank';
const PORT = Number(process.env.PORT ?? 8090);

if (process.env.ALLOW_DEV_AUTH !== 'true') {
  console.error(
    'La autenticación corporativa no está integrada.\n' +
      'Para levantar la consola en desarrollo: ALLOW_DEV_AUTH=true',
  );
  process.exit(1);
}

const pool = new pg.Pool({ connectionString: DATABASE_URL, max: 4 });
const audit = new AuditLog(pool);
await audit.migrate();

const authenticator = new SimAuthenticator();
const token = authenticator.issue('dev@aibank.local', ['reconciliation_analyst']);
console.warn('AUTENTICACIÓN DE DESARROLLO ACTIVA — no usar fuera de local');
console.warn(`token: ${token}`);

const core = connectCore(CORE_ADDR);
const server = createServer({ core, audit, authenticator });

server.listen(PORT, () => console.log(`consola escuchando en :${PORT}`));

process.on('SIGINT', () => {
  server.close();
  core.close();
  void pool.end();
});

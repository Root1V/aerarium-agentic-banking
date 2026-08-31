/**
 * Autenticador de desarrollo para la consola.
 *
 * NO es un sistema de autenticación: no valida credenciales ni expira sesiones.
 * El acceso del personal interno se resuelve con el proveedor de identidad
 * corporativo y segundo factor obligatorio.
 *
 * Vive en un paquete aparte y marcado para que no pueda cablearse a producción
 * por descuido.
 */

import { randomUUID } from 'node:crypto';
import { UnauthenticatedError, type Authenticator, type Operator, type Role } from '../auth.ts';

export class SimAuthenticator implements Authenticator {
  private readonly tokens = new Map<string, Operator>();

  /** Emite un token para una persona con los roles indicados. */
  issue(email: string, roles: readonly Role[]): string {
    const token = `dev-${randomUUID()}`;
    this.tokens.set(token, { id: randomUUID(), email, roles });
    return token;
  }

  async authenticate(token: string): Promise<Operator> {
    const operator = this.tokens.get(token);
    if (!operator) throw new UnauthenticatedError();
    return operator;
  }
}

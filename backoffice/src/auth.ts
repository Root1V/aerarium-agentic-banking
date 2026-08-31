/**
 * Identidad y permisos del personal interno.
 *
 * El fraude interno es un vector real en banca: quien opera la consola puede
 * mover dinero, cerrar diferencias o aprobar clientes. Por eso aquí no hay
 * "usuario administrador" genérico — cada acción exige un permiso concreto y
 * queda atribuida a una persona.
 */

/** Permisos, definidos por lo que habilitan y no por cargo. */
export type Permission =
  | 'reconciliation:read'
  | 'reconciliation:resolve'
  | 'reconciliation:run';

export type Role = 'viewer' | 'reconciliation_analyst' | 'auditor';

/**
 * Un `viewer` ve pero no decide, y un `auditor` tampoco: quien audita no debe
 * poder alterar lo que audita. Solo el analista de conciliación resuelve.
 */
const ROLE_PERMISSIONS: Record<Role, readonly Permission[]> = {
  viewer: ['reconciliation:read'],
  auditor: ['reconciliation:read'],
  reconciliation_analyst: [
    'reconciliation:read',
    'reconciliation:resolve',
    'reconciliation:run',
  ],
};

/** Persona autenticada que opera la consola. */
export interface Operator {
  readonly id: string;
  /** Se registra en la auditoría: una acción sin persona detrás no sirve. */
  readonly email: string;
  readonly roles: readonly Role[];
}

export function can(operator: Operator, permission: Permission): boolean {
  return operator.roles.some((role) =>
    ROLE_PERMISSIONS[role]?.includes(permission),
  );
}

export class UnauthenticatedError extends Error {
  constructor() {
    super('no autenticado');
    this.name = 'UnauthenticatedError';
  }
}

/**
 * Resuelve un token a la persona que opera.
 *
 * Es un PUERTO, no una implementación: el acceso del personal interno se
 * resuelve con el proveedor de identidad corporativo (SSO con segundo factor
 * obligatorio), no con credenciales propias de esta consola.
 */
export interface Authenticator {
  authenticate(token: string): Promise<Operator>;
}

/** Extrae el token del encabezado Authorization. */
export function bearerToken(header: string | undefined): string {
  if (!header) return '';
  const prefix = 'bearer ';
  if (header.length <= prefix.length) return '';
  if (header.slice(0, prefix.length).toLowerCase() !== prefix) return '';
  return header.slice(prefix.length).trim();
}

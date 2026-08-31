package com.aibank.core.ledger

import java.time.OffsetDateTime
import java.util.UUID

enum class AccountType { ASSET, LIABILITY, EQUITY, INCOME, EXPENSE }
enum class AccountOwner { CUSTOMER, INTERNAL }
enum class AccountStatus { ACTIVE, FROZEN, CLOSED }
enum class Direction { DEBIT, CREDIT }

data class Account(
    val id: UUID,
    val code: String,
    val name: String,
    val type: AccountType,
    val owner: AccountOwner,
    val ownerId: UUID?,
    val currency: String,
    val status: AccountStatus,
    val createdAt: OffsetDateTime,
)

/** Orden de asiento dentro de una solicitud de posting. Montos en unidades menores (centavos). */
data class EntryCommand(
    val accountId: UUID,
    val direction: Direction,
    val amountMinor: Long,
    val currency: String,
)

/**
 * Solicitud de transacción contable. `idempotencyKey` la define quien origina la
 * operación (transferencia, webhook de tarjeta, etc.) y garantiza efecto único.
 */
data class PostingRequest(
    val idempotencyKey: String,
    val kind: String,
    val entries: List<EntryCommand>,
    val description: String? = null,
    val metadataJson: String = "{}",
)

data class LedgerTransaction(
    val id: UUID,
    val idempotencyKey: String,
    val kind: String,
    val description: String?,
    val postedAt: OffsetDateTime,
)

data class PostingResult(
    val transaction: LedgerTransaction,
    /** true si la clave ya existía y se devolvió la transacción original (replay idempotente). */
    val replayed: Boolean,
)

/** La solicitud viola una regla del ledger (desbalance, montos inválidos, monedas). */
class InvalidPostingException(message: String) : IllegalArgumentException(message)

/** La idempotency key ya fue usada con un payload DIFERENTE: error de programación del llamador. */
class IdempotencyConflictException(key: String) :
    IllegalStateException("idempotency key '$key' was already used with a different payload")

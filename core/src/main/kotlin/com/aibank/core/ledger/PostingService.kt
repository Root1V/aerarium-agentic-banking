package com.aibank.core.ledger

import com.aibank.core.db.Database.tx
import java.security.MessageDigest
import java.sql.Connection
import java.time.OffsetDateTime
import java.util.UUID
import javax.sql.DataSource

/**
 * Motor de posting: la ÚNICA vía de entrada de movimientos al ledger.
 *
 * Garantías:
 *  - Validación de negocio antes de tocar la base (2+ asientos, montos > 0, balance por moneda).
 *  - Idempotencia: la misma clave produce exactamente una transacción; un replay devuelve
 *    la original. La misma clave con payload distinto es un error del llamador.
 *  - Atomicidad: transacción + asientos se escriben en una sola transacción de base de datos;
 *    los triggers del esquema re-verifican las invariantes al commit (defensa en profundidad).
 */
class PostingService(private val dataSource: DataSource) {

    fun post(request: PostingRequest): PostingResult {
        validate(request)
        val hash = requestHash(request)

        return dataSource.tx { conn ->
            val inserted = insertTransaction(conn, request, hash)
            if (inserted == null) {
                // La clave ya existe: replay o conflicto. (ON CONFLICT espera a que la
                // transacción concurrente dueña de la clave termine antes de resolver.)
                val existing = findByIdempotencyKey(conn, request.idempotencyKey)
                    ?: error("idempotency key vanished — concurrent rollback; caller should retry")
                if (existing.second != hash) throw IdempotencyConflictException(request.idempotencyKey)
                PostingResult(existing.first, replayed = true)
            } else {
                insertEntries(conn, inserted.id, request.entries)
                PostingResult(inserted, replayed = false)
            }
        }
    }

    // ------------------------------------------------------------------ validación

    private fun validate(request: PostingRequest) {
        if (request.idempotencyKey.isBlank()) throw InvalidPostingException("idempotencyKey must not be blank")
        if (request.kind.isBlank()) throw InvalidPostingException("kind must not be blank")
        if (request.entries.size < 2) throw InvalidPostingException("a transaction requires at least 2 entries")
        request.entries.forEach {
            if (it.amountMinor <= 0) throw InvalidPostingException("amounts must be positive, got ${it.amountMinor}")
            if (it.currency.length != 3) throw InvalidPostingException("currency must be ISO-4217, got '${it.currency}'")
        }
        request.entries.groupBy { it.currency }.forEach { (currency, entries) ->
            val debits = entries.filter { it.direction == Direction.DEBIT }.sumOf { it.amountMinor }
            val credits = entries.filter { it.direction == Direction.CREDIT }.sumOf { it.amountMinor }
            if (debits != credits) {
                throw InvalidPostingException(
                    "unbalanced transaction for $currency: debits=$debits credits=$credits"
                )
            }
        }
    }

    /** Hash canónico del payload para detectar reutilización de clave con contenido distinto. */
    private fun requestHash(request: PostingRequest): String {
        val canonical = buildString {
            append(request.kind).append('|')
            request.entries
                .map { "${it.accountId}:${it.direction}:${it.amountMinor}:${it.currency}" }
                .sorted()
                .forEach { append(it).append(';') }
        }
        val digest = MessageDigest.getInstance("SHA-256").digest(canonical.toByteArray())
        return digest.joinToString("") { "%02x".format(it) }
    }

    // ------------------------------------------------------------------ persistencia

    private fun insertTransaction(conn: Connection, request: PostingRequest, hash: String): LedgerTransaction? {
        conn.prepareStatement(
            """
            INSERT INTO ledger_transactions (idempotency_key, request_hash, kind, description, metadata)
            VALUES (?, ?, ?, ?, ?::jsonb)
            ON CONFLICT (idempotency_key) DO NOTHING
            RETURNING id, idempotency_key, kind, description, posted_at
            """.trimIndent()
        ).use { st ->
            st.setString(1, request.idempotencyKey)
            st.setString(2, hash)
            st.setString(3, request.kind)
            st.setString(4, request.description)
            st.setString(5, request.metadataJson)
            st.executeQuery().use { rs ->
                return if (rs.next()) rs.toTransaction() else null
            }
        }
    }

    private fun insertEntries(conn: Connection, transactionId: UUID, entries: List<EntryCommand>) {
        conn.prepareStatement(
            """
            INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_minor, currency)
            VALUES (?, ?, ?::entry_direction, ?, ?)
            """.trimIndent()
        ).use { st ->
            entries.forEach { e ->
                st.setObject(1, transactionId)
                st.setObject(2, e.accountId)
                st.setString(3, e.direction.name)
                st.setLong(4, e.amountMinor)
                st.setString(5, e.currency)
                st.addBatch()
            }
            st.executeBatch()
        }
    }

    private fun findByIdempotencyKey(conn: Connection, key: String): Pair<LedgerTransaction, String>? {
        conn.prepareStatement(
            """
            SELECT id, idempotency_key, kind, description, posted_at, request_hash
            FROM ledger_transactions WHERE idempotency_key = ?
            """.trimIndent()
        ).use { st ->
            st.setString(1, key)
            st.executeQuery().use { rs ->
                return if (rs.next()) rs.toTransaction() to rs.getString("request_hash") else null
            }
        }
    }

    private fun java.sql.ResultSet.toTransaction() = LedgerTransaction(
        id = getObject("id", UUID::class.java),
        idempotencyKey = getString("idempotency_key"),
        kind = getString("kind"),
        description = getString("description"),
        postedAt = getObject("posted_at", OffsetDateTime::class.java),
    )
}

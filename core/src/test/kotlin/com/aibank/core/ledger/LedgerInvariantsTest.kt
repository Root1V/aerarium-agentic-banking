package com.aibank.core.ledger

import com.aibank.core.db.Database
import com.aibank.core.db.Database.tx
import org.junit.jupiter.api.AfterAll
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestInstance
import org.testcontainers.containers.PostgreSQLContainer
import java.sql.SQLException
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import javax.sql.DataSource
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * Invariantes del ledger, probadas contra PostgreSQL real (Testcontainers):
 *  1. Doble partida: solo se aceptan transacciones balanceadas (capa servicio Y capa DB).
 *  2. Append-only: UPDATE/DELETE sobre transacciones/asientos falla.
 *  3. Idempotencia: la misma clave = una sola transacción, incluso bajo concurrencia.
 *  4. Moneda del asiento = moneda de la cuenta.
 *  5. Los saldos son proyección exacta de los asientos.
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
class LedgerInvariantsTest {

    private lateinit var container: PostgreSQLContainer<*>
    private lateinit var dataSource: DataSource
    private lateinit var posting: PostingService
    private val accounts = AccountRepository()

    @BeforeAll
    fun setUp() {
        container = PostgreSQLContainer("postgres:16-alpine")
        container.start()
        dataSource = Database.connect(container.jdbcUrl, container.username, container.password, maxPoolSize = 12)
        Database.migrate(dataSource)
        posting = PostingService(dataSource)
    }

    @AfterAll
    fun tearDown() {
        (dataSource as? AutoCloseable)?.close()
        container.stop()
    }

    private fun newAccounts(suffix: String = UUID.randomUUID().toString().take(8)): Pair<Account, Account> =
        dataSource.tx { conn ->
            val cash = accounts.create(
                conn, "cash-$suffix", "Caja operativa", AccountType.ASSET, AccountOwner.INTERNAL, null, "USD"
            )
            val customer = accounts.create(
                conn, "cust-$suffix", "Cliente", AccountType.LIABILITY, AccountOwner.CUSTOMER, UUID.randomUUID(), "USD"
            )
            cash to customer
        }

    private fun deposit(cash: Account, customer: Account, amount: Long, key: String) = posting.post(
        PostingRequest(
            idempotencyKey = key,
            kind = "deposit",
            entries = listOf(
                EntryCommand(cash.id, Direction.DEBIT, amount, "USD"),
                EntryCommand(customer.id, Direction.CREDIT, amount, "USD"),
            ),
        )
    )

    // ------------------------------------------------------------ 1. doble partida

    @Test
    fun `posting balanceado actualiza saldos como proyeccion del ledger`() {
        val (cash, customer) = newAccounts()
        deposit(cash, customer, 150_00, "dep-${UUID.randomUUID()}")
        deposit(cash, customer, 50_00, "dep-${UUID.randomUUID()}")

        dataSource.tx { conn ->
            assertEquals(200_00, accounts.balanceMinor(conn, cash.id), "activo crece al debe")
            assertEquals(200_00, accounts.balanceMinor(conn, customer.id), "pasivo (saldo del cliente) crece al haber")
        }
    }

    @Test
    fun `posting desbalanceado es rechazado por el servicio`() {
        val (cash, customer) = newAccounts()
        assertFailsWith<InvalidPostingException> {
            posting.post(
                PostingRequest(
                    idempotencyKey = "bad-${UUID.randomUUID()}",
                    kind = "deposit",
                    entries = listOf(
                        EntryCommand(cash.id, Direction.DEBIT, 100_00, "USD"),
                        EntryCommand(customer.id, Direction.CREDIT, 99_00, "USD"),
                    ),
                )
            )
        }
    }

    @Test
    fun `la base de datos rechaza al commit un insert desbalanceado que salte el servicio`() {
        val (cash, _) = newAccounts()
        val ex = assertFailsWith<SQLException> {
            dataSource.tx { conn ->
                conn.prepareStatement(
                    """
                    WITH t AS (
                        INSERT INTO ledger_transactions (idempotency_key, request_hash, kind)
                        VALUES (?, 'x', 'rogue') RETURNING id
                    )
                    INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_minor, currency)
                    SELECT id, ?, 'DEBIT'::entry_direction, 100, 'USD' FROM t
                    """.trimIndent()
                ).use { st ->
                    st.setString(1, "rogue-${UUID.randomUUID()}")
                    st.setObject(2, cash.id)
                    st.executeUpdate()
                }
            }
        }
        assertTrue(ex.message!!.contains("unbalanced") || ex.message!!.contains("at least 2"))
    }

    @Test
    fun `un monto negativo o cero es rechazado`() {
        val (cash, customer) = newAccounts()
        assertFailsWith<InvalidPostingException> {
            posting.post(
                PostingRequest(
                    idempotencyKey = "neg-${UUID.randomUUID()}",
                    kind = "deposit",
                    entries = listOf(
                        EntryCommand(cash.id, Direction.DEBIT, -5, "USD"),
                        EntryCommand(customer.id, Direction.CREDIT, -5, "USD"),
                    ),
                )
            )
        }
    }

    // ------------------------------------------------------------ 2. append-only

    @Test
    fun `los asientos y transacciones no admiten UPDATE ni DELETE`() {
        val (cash, customer) = newAccounts()
        val result = deposit(cash, customer, 10_00, "imm-${UUID.randomUUID()}")

        listOf(
            "UPDATE ledger_entries SET amount_minor = 1 WHERE transaction_id = ?",
            "DELETE FROM ledger_entries WHERE transaction_id = ?",
            "UPDATE ledger_transactions SET kind = 'hacked' WHERE id = ?",
            "DELETE FROM ledger_transactions WHERE id = ?",
        ).forEach { sql ->
            val ex = assertFailsWith<SQLException>("debería fallar: $sql") {
                dataSource.tx { conn ->
                    conn.prepareStatement(sql).use { st ->
                        st.setObject(1, result.transaction.id)
                        st.executeUpdate()
                    }
                }
            }
            assertTrue(ex.message!!.contains("append-only"), "mensaje inesperado: ${ex.message}")
        }
    }

    // ------------------------------------------------------------ 3. idempotencia

    @Test
    fun `replay con la misma clave devuelve la transaccion original sin duplicar asientos`() {
        val (cash, customer) = newAccounts()
        val key = "rep-${UUID.randomUUID()}"

        val first = deposit(cash, customer, 30_00, key)
        val second = deposit(cash, customer, 30_00, key)

        assertEquals(first.transaction.id, second.transaction.id)
        assertTrue(second.replayed)
        dataSource.tx { conn ->
            assertEquals(30_00, accounts.balanceMinor(conn, customer.id), "el saldo refleja UN solo efecto")
        }
    }

    @Test
    fun `la misma clave con payload distinto es un conflicto explicito`() {
        val (cash, customer) = newAccounts()
        val key = "conf-${UUID.randomUUID()}"
        deposit(cash, customer, 30_00, key)

        assertFailsWith<IdempotencyConflictException> {
            deposit(cash, customer, 99_99, key)
        }
    }

    @Test
    fun `bajo concurrencia N intentos con la misma clave producen exactamente una transaccion`() {
        val (cash, customer) = newAccounts()
        val key = "conc-${UUID.randomUUID()}"
        val threads = 10
        val latch = CountDownLatch(1)
        val pool = Executors.newFixedThreadPool(threads)

        val futures = (1..threads).map {
            pool.submit<PostingResult> {
                latch.await()
                deposit(cash, customer, 77_00, key)
            }
        }
        latch.countDown()
        val results = futures.map { it.get() }
        pool.shutdown()

        val txIds = results.map { it.transaction.id }.toSet()
        assertEquals(1, txIds.size, "todas las respuestas apuntan a la misma transacción")
        assertEquals(threads - 1, results.count { it.replayed }, "solo una fue insert real")
        dataSource.tx { conn ->
            assertEquals(77_00, accounts.balanceMinor(conn, customer.id), "efecto único pese a $threads intentos")
            conn.prepareStatement("SELECT COUNT(*) FROM ledger_entries WHERE transaction_id = ?").use { st ->
                st.setObject(1, txIds.first())
                st.executeQuery().use { rs -> rs.next(); assertEquals(2, rs.getInt(1)) }
            }
        }
    }

    // ------------------------------------------------------------ 4. monedas

    @Test
    fun `un asiento en moneda distinta a la de la cuenta es rechazado`() {
        val (cash, customer) = newAccounts()
        val ex = assertFailsWith<SQLException> {
            posting.post(
                PostingRequest(
                    idempotencyKey = "fx-${UUID.randomUUID()}",
                    kind = "deposit",
                    entries = listOf(
                        EntryCommand(cash.id, Direction.DEBIT, 10_00, "EUR"),
                        EntryCommand(customer.id, Direction.CREDIT, 10_00, "EUR"),
                    ),
                )
            )
        }
        assertTrue(ex.message!!.contains("does not match account currency"))
    }

    // ------------------------------------------------------------ 5. proyección

    @Test
    fun `una transferencia entre clientes mueve saldos y la caja queda intacta`() {
        val (cash, alice) = newAccounts()
        val bob = dataSource.tx { conn ->
            accounts.create(
                conn, "cust-bob-${UUID.randomUUID().toString().take(8)}", "Bob",
                AccountType.LIABILITY, AccountOwner.CUSTOMER, UUID.randomUUID(), "USD"
            )
        }
        deposit(cash, alice, 100_00, "dep-${UUID.randomUUID()}")

        posting.post(
            PostingRequest(
                idempotencyKey = "xfer-${UUID.randomUUID()}",
                kind = "p2p_transfer",
                entries = listOf(
                    EntryCommand(alice.id, Direction.DEBIT, 40_00, "USD"),
                    EntryCommand(bob.id, Direction.CREDIT, 40_00, "USD"),
                ),
            )
        )

        dataSource.tx { conn ->
            assertEquals(60_00, accounts.balanceMinor(conn, alice.id))
            assertEquals(40_00, accounts.balanceMinor(conn, bob.id))
            assertEquals(100_00, accounts.balanceMinor(conn, cash.id), "la caja no cambia en un P2P interno")
        }
        assertNotNull(dataSource.tx { conn -> accounts.findByCode(conn, cash.code) })
    }
}

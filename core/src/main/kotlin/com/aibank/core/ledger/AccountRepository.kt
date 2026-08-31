package com.aibank.core.ledger

import java.sql.Connection
import java.sql.ResultSet
import java.time.OffsetDateTime
import java.util.UUID

class AccountRepository {

    fun create(
        conn: Connection,
        code: String,
        name: String,
        type: AccountType,
        owner: AccountOwner,
        ownerId: UUID?,
        currency: String,
    ): Account {
        require(currency.length == 3) { "currency must be ISO-4217 (3 letters), got '$currency'" }
        conn.prepareStatement(
            """
            INSERT INTO accounts (code, name, type, owner, owner_id, currency)
            VALUES (?, ?, ?::account_type, ?::account_owner, ?, ?)
            RETURNING id, code, name, type, owner, owner_id, currency, status, created_at
            """.trimIndent()
        ).use { st ->
            st.setString(1, code)
            st.setString(2, name)
            st.setString(3, type.name)
            st.setString(4, owner.name)
            st.setObject(5, ownerId)
            st.setString(6, currency)
            st.executeQuery().use { rs ->
                rs.next()
                return rs.toAccount()
            }
        }
    }

    fun findByCode(conn: Connection, code: String): Account? {
        conn.prepareStatement(
            "SELECT id, code, name, type, owner, owner_id, currency, status, created_at FROM accounts WHERE code = ?"
        ).use { st ->
            st.setString(1, code)
            st.executeQuery().use { rs ->
                return if (rs.next()) rs.toAccount() else null
            }
        }
    }

    /** Saldo natural de la cuenta como proyección del ledger (vista account_balances). */
    fun balanceMinor(conn: Connection, accountId: UUID): Long {
        conn.prepareStatement("SELECT balance_minor FROM account_balances WHERE account_id = ?").use { st ->
            st.setObject(1, accountId)
            st.executeQuery().use { rs ->
                require(rs.next()) { "account $accountId not found" }
                return rs.getLong(1)
            }
        }
    }

    private fun ResultSet.toAccount() = Account(
        id = getObject("id", UUID::class.java),
        code = getString("code"),
        name = getString("name"),
        type = AccountType.valueOf(getString("type")),
        owner = AccountOwner.valueOf(getString("owner")),
        ownerId = getObject("owner_id", UUID::class.java),
        currency = getString("currency"),
        status = AccountStatus.valueOf(getString("status")),
        createdAt = getObject("created_at", OffsetDateTime::class.java),
    )
}

package com.aibank.core.db

import com.zaxxer.hikari.HikariConfig
import com.zaxxer.hikari.HikariDataSource
import org.flywaydb.core.Flyway
import java.sql.Connection
import javax.sql.DataSource

object Database {

    fun connect(jdbcUrl: String, user: String, password: String, maxPoolSize: Int = 10): DataSource {
        val config = HikariConfig().apply {
            this.jdbcUrl = jdbcUrl
            username = user
            this.password = password
            maximumPoolSize = maxPoolSize
            isAutoCommit = false
            transactionIsolation = "TRANSACTION_READ_COMMITTED"
        }
        return HikariDataSource(config)
    }

    fun migrate(dataSource: DataSource) {
        Flyway.configure()
            .dataSource(dataSource)
            .locations("classpath:db/migration")
            .load()
            .migrate()
    }

    /** Ejecuta [block] dentro de una transacción de base de datos con commit/rollback correcto. */
    fun <T> DataSource.tx(block: (Connection) -> T): T {
        connection.use { conn ->
            conn.autoCommit = false
            try {
                val result = block(conn)
                conn.commit()
                return result
            } catch (e: Exception) {
                runCatching { conn.rollback() }
                throw e
            }
        }
    }
}

-- =============================================================================
-- AIBank core — V003: outbox transaccional
--
-- El problema que resuelve: si el core escribiera en la base y LUEGO publicara en
-- Kafka, una caída entre ambos pasos dejaría un asiento sin evento (notificación
-- perdida, antifraude que nunca corrió, hueco de conciliación) o un evento de una
-- transacción que se revirtió. Eso es la "doble escritura" y no tiene arreglo
-- posterior fiable.
--
-- La solución: el evento se escribe AQUÍ, en la misma transacción que los asientos.
-- Un proceso relay lo publica después y lo marca. Si el relay cae tras publicar y
-- antes de marcar, republica: la entrega es AT-LEAST-ONCE y los consumidores
-- deduplican por event_id.
-- =============================================================================

CREATE TABLE outbox (
    -- Identidad monotónica: define el orden de publicación.
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- Identidad estable del evento, para deduplicación en el consumidor.
    event_id       UUID NOT NULL UNIQUE,
    event_type     TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    -- Clave de partición: garantiza orden por agregado en el bus.
    aggregate_id   UUID NOT NULL,
    -- Evento serializado con el contrato de contracts/proto/aibank/events.
    -- Se guardan bytes en vez de JSON para que el esquema sea el mismo que cruza
    -- la frontera gRPC: un solo contrato verificado por el compilador en ambos
    -- lados. Las columnas de arriba dan visibilidad operativa sin decodificar.
    payload        BYTEA NOT NULL,
    occurred_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ,
    attempts       INT NOT NULL DEFAULT 0,
    last_error     TEXT
);

-- El relay solo mira lo no publicado: índice parcial para que la cola no se
-- degrade a medida que la tabla crece con el histórico.
CREATE INDEX idx_outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- Consulta de auditoría: qué eventos generó una transacción.
CREATE INDEX idx_outbox_aggregate ON outbox (aggregate_type, aggregate_id);

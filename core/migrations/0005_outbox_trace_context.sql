-- =============================================================================
-- AIBank core — V005: contexto de traza en el outbox
--
-- El evento se publica en OTRO proceso (el relay) y minutos más tarde. Sin
-- guardar el contexto de traza junto al evento, la traza de la operación termina
-- en el COMMIT y no hay forma de enlazar la publicación —ni lo que hagan los
-- consumidores— con el pago que la originó.
--
-- Se guarda el `traceparent` del estándar W3C: texto corto, sin datos personales.
-- =============================================================================

ALTER TABLE outbox ADD COLUMN trace_context TEXT;

COMMENT ON COLUMN outbox.trace_context IS
    'traceparent W3C de la transacción que originó el evento; enlaza la publicación asíncrona con su origen';

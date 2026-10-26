-- Índice de las autorizaciones por su asiento de retención.
--
-- El historial de la app agrupa los pagos de cada plataforma: "Mercatus · 120
-- pagos". Para eso, cada movimiento de retención tiene que saber de qué mandato
-- salió, y se llega a él por el asiento. Sin índice, cada carga de la pantalla de
-- inicio recorrería la tabla de autorizaciones entera.
CREATE INDEX idx_authorizations_hold_transaction
    ON authorizations (hold_transaction_id);

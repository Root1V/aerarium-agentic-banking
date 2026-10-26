-- Índice de las retenciones vivas por cuenta pagadora.
--
-- La pantalla de inicio de la app muestra cuánto tiene retenido cada persona en
-- pagos en curso, y es la consulta más frecuente del banco. El índice que ya
-- había (payer_account_id, created_at) sirve, pero recorre TODO el historial de
-- la cuenta para quedarse con las pocas autorizaciones vivas: un titular cuyo
-- agente paga por llamada acumula miles en semanas.
--
-- Parcial a propósito: solo contiene lo que está en AUTHORIZED, que es una
-- ventana de 15 minutos por autorización. Se mantiene pequeño sin importar
-- cuánto historial tenga la cuenta, y una autorización sale del índice sola al
-- capturarse o liberarse.
CREATE INDEX idx_authorizations_live_by_payer
    ON authorizations (payer_account_id)
    WHERE status = 'AUTHORIZED';

-- Para qué es cada bolsillo (F3-02). El bolsillo en sí es una cuenta hija del
-- core; aquí solo vive lo que el core no necesita saber.

CREATE SCHEMA IF NOT EXISTS pockets;

CREATE TABLE IF NOT EXISTS pockets.pockets (
    account_id        UUID PRIMARY KEY,
    parent_account_id UUID NOT NULL,
    customer_id       UUID NOT NULL,
    -- `card`: lo apartado para pagar la tarjeta. La previsión de saldo lo da por
    -- reservado para eso. `goal`: el de una meta (F3-03), que abre y cierra el
    -- servicio de metas. `general`: cualquier otro fin.
    purpose           TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- El propósito se valida aparte para poder ampliarlo en una base que ya existe.
ALTER TABLE pockets.pockets DROP CONSTRAINT IF EXISTS pockets_purpose_check;
ALTER TABLE pockets.pockets ADD CONSTRAINT pockets_purpose_check
    CHECK (purpose IN ('card', 'general', 'goal'));

-- Un solo bolsillo para la tarjeta por cuenta: dos harían ambiguo de dónde se paga.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pockets_card
    ON pockets.pockets (parent_account_id) WHERE purpose = 'card';

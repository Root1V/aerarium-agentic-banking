-- Para qué es cada bolsillo (F3-02). El bolsillo en sí es una cuenta hija del
-- core; aquí solo vive lo que el core no necesita saber.

CREATE SCHEMA IF NOT EXISTS pockets;

CREATE TABLE IF NOT EXISTS pockets.pockets (
    account_id        UUID PRIMARY KEY,
    parent_account_id UUID NOT NULL,
    customer_id       UUID NOT NULL,
    -- `card`: lo apartado para pagar la tarjeta. La previsión de saldo lo da por
    -- reservado para eso. `general`: cualquier otro fin.
    purpose           TEXT NOT NULL CHECK (purpose IN ('card', 'general')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Un solo bolsillo para la tarjeta por cuenta: dos harían ambiguo de dónde se paga.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pockets_card
    ON pockets.pockets (parent_account_id) WHERE purpose = 'card';

-- One row per ticket. seq numbers the tickets of an order from 1 to its
-- quantity.
--
-- The unique constraint is a second guard behind processed_events: whatever
-- arrives, and however often, an order can never hold more tickets than it
-- paid for.
CREATE TABLE tickets (
    id        uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id  uuid        NOT NULL,
    drop_id   text        NOT NULL,
    tier      text        NOT NULL,
    seq       integer     NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_id, seq)
);

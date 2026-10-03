-- One row per order that was charged or declined.
--
-- The unique order_id is a second guard behind processed_events: whatever
-- arrives, and however often, an order has one payment outcome.
CREATE TABLE payments (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id   uuid        NOT NULL UNIQUE,
    status     text        NOT NULL,
    reason     text,
    created_at timestamptz NOT NULL DEFAULT now()
);

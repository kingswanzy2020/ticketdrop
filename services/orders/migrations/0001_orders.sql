-- One row per order. An order starts as 'pending' and becomes 'ticketed' when
-- fulfillment reports that its tickets were issued.
CREATE TABLE orders (
    id             uuid        PRIMARY KEY,
    drop_id        text        NOT NULL,
    tier           text        NOT NULL,
    quantity       integer     NOT NULL CHECK (quantity > 0),
    customer_email text        NOT NULL,
    status         text        NOT NULL DEFAULT 'pending',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

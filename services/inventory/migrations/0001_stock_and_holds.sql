-- How many tickets each tier of a drop has, and how many can still be taken.
--
-- available = capacity - tickets held - tickets sold. It is stored instead of
-- computed, so that taking tickets is one atomic UPDATE of one row.
--
-- The CHECK is the last guard against overselling. A statement that would take
-- more tickets than are left fails in the database, whatever the code that
-- issued it believed.
CREATE TABLE stock (
    drop_id   text    NOT NULL,
    tier      text    NOT NULL,
    capacity  integer NOT NULL CHECK (capacity >= 0),
    available integer NOT NULL CHECK (available >= 0 AND available <= capacity),
    PRIMARY KEY (drop_id, tier)
);

-- Tickets set aside for one order. An order has at most one hold.
--
--   held       taken out of stock, waiting for the payment
--   confirmed  paid for: the tickets are sold
--   released   the payment failed: the tickets went back into stock
CREATE TABLE holds (
    id         uuid        PRIMARY KEY,
    order_id   uuid        NOT NULL UNIQUE,
    drop_id    text        NOT NULL,
    tier       text        NOT NULL,
    quantity   integer     NOT NULL CHECK (quantity > 0),
    status     text        NOT NULL DEFAULT 'held',
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    settled_at timestamptz,
    FOREIGN KEY (drop_id, tier) REFERENCES stock (drop_id, tier)
);

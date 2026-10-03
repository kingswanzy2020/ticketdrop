-- A drop is one sale: an event at a venue whose tickets go on sale at a set time.
--
--   scheduled  announced; its details can still change; nothing can be ordered
--   open       on sale; its tiers and capacities are fixed
CREATE TABLE drops (
    id         text        PRIMARY KEY,
    name       text        NOT NULL,
    venue      text        NOT NULL,
    opens_at   timestamptz NOT NULL,
    status     text        NOT NULL DEFAULT 'scheduled',
    created_at timestamptz NOT NULL DEFAULT now(),
    opened_at  timestamptz
);

-- The scheduler asks every second which drops are due to open. This index
-- covers only the ones still waiting.
CREATE INDEX drops_due ON drops (opens_at) WHERE status = 'scheduled';

-- The kinds of ticket a drop sells, with the price and number of each.
CREATE TABLE tiers (
    drop_id     text    NOT NULL REFERENCES drops (id) ON DELETE CASCADE,
    tier        text    NOT NULL,
    price_cents integer NOT NULL CHECK (price_cents >= 0),
    capacity    integer NOT NULL CHECK (capacity > 0),
    PRIMARY KEY (drop_id, tier)
);

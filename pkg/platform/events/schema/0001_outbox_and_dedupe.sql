-- Events waiting to be published. A row is written in the same transaction as
-- the state change it describes; the relay publishes it afterwards.
CREATE TABLE outbox (
    id           bigserial   PRIMARY KEY,
    event_id     uuid        NOT NULL UNIQUE,
    event_type   text        NOT NULL,
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);

-- The relay only ever asks for unpublished rows, so index only those.
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- Events this service has already handled. SQS delivers at least once, so a
-- handler records the event ID in the same transaction as its effects and
-- skips any event it finds here.
CREATE TABLE processed_events (
    event_id     uuid        PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);

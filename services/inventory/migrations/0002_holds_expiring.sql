-- The expiry sweep looks for holds that are still held and past their time,
-- several times a minute. This index covers exactly those rows, so the sweep
-- does not read every hold ever made.
CREATE INDEX holds_expiring ON holds (expires_at) WHERE status = 'held';

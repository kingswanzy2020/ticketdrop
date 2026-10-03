-- Why a failed order failed, such as a declined card or a hold that expired.
-- Empty for every other order.
ALTER TABLE orders ADD COLUMN failure_reason text;

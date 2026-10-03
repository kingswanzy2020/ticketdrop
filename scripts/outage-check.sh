#!/usr/bin/env bash
# Simulates a payment-provider outage on the local stack (`make up`) and proves
# that nothing is left stuck:
#
#   1. the scheduler survives losing its leader: the standby takes over
#   2. orders that cannot be paid for fail when their hold expires, and their
#      tickets return to stock
#   3. each customer is told that their order failed, once
#   4. when the provider recovers and the dead-lettered payments are retried,
#      nobody is charged for an order that has already failed, or told again
#
# It restarts payments and inventory with outage settings (a provider that is
# down, and holds that last 40 seconds instead of 10 minutes), and puts both
# back on the way out. Allow about two minutes.
#
# Settings, as environment variables:
#   ORDERS  orders placed during the outage (default 5; keep it at 10 or fewer)
set -euo pipefail

cd "$(dirname "$0")/.."
source scripts/lib.sh

ORDERS=${ORDERS:-5}
CAPACITY=$((ORDERS * 4))
# Long enough for payments to give up on each order (three tries, about 30s)
# before the hold runs out. A hold that expired sooner would be noticed by
# payments on a retry, and the orders would never reach the dead-letter queue.
OUTAGE_HOLD_TTL=40s
OUTAGE_HOLD_GRACE=5s
DROP=outage-$(date +%s)
TIER=general

none_pending() { [ "$(orders_in pending)" -eq 0 ]; }
refused_at_least() {
  [ "$(sql payments "SELECT count(*) FROM outbox WHERE event_type = 'payment.failed' AND payload->'data'->>'drop_id' = '$DROP' AND payload->'data'->>'reason' = 'hold expired'")" -ge "$1" ]
}
new_leader() {
  local now
  now=$(leaders)
  [ -n "$now" ] && [ "$now" != "$1" ]
}

# Back to the normal settings, with both scheduler replicas running.
restore() {
  compose up -d --wait payments inventory scheduler >/dev/null 2>&1 || true
}
trap restore EXIT

require_stack

echo "outage check: $ORDERS orders in drop $DROP while the payment provider is down"
echo

# --- the provider goes down ----------------------------------------------------

PAYMENT_ERROR_RATE=1 HOLD_TTL=$OUTAGE_HOLD_TTL HOLD_GRACE=$OUTAGE_HOLD_GRACE \
  compose up -d --wait payments inventory >/dev/null 2>&1
wait_for 30 ready payments && wait_for 30 ready inventory ||
  { echo "payments and inventory did not come back with the outage settings" >&2; exit 2; }

create_drop "$CAPACITY" "$(date +%s)"
wait_for 30 on_sale || { echo "the drop did not go on sale" >&2; exit 2; }

dead_before=$(dead_letters payments)
notified_before=$(notified)
placed=$SECONDS
accepted=0
for n in $(seq "$ORDERS"); do
  if [ "$(place "$n")" = 201 ]; then accepted=$((accepted + 1)); fi
done
echo "  placed $ORDERS orders with the provider down: $accepted accepted"
held=$((CAPACITY - $(available)))
echo "  $held of $CAPACITY tickets are now held for orders that cannot be paid for"

# --- the scheduler loses its leader --------------------------------------------

old_leader=$(leaders)
docker kill -s KILL "$old_leader" >/dev/null
killed=$SECONDS
if wait_for 30 new_leader "$old_leader"; then
  took_over=$((SECONDS - killed))
  echo "  killed the scheduler's leader; the standby took over ${took_over}s later"
else
  took_over=""
  echo "  killed the scheduler's leader; no standby took over within 30s"
fi

# --- the holds run out ---------------------------------------------------------

# Nothing here releases a hold. The new leader tells inventory every few
# seconds, inventory publishes hold.expired, and orders fails the order.
if wait_for 150 none_pending; then
  echo "  every order had failed $((SECONDS - placed))s after it was placed"
else
  echo "  orders still pending after $((SECONDS - placed))s"
fi
echo

check "orders accepted while the provider was down" "$accepted" "$ORDERS"
if [ -n "$took_over" ]; then
  pass "exactly one scheduler replica leads after the kill, and it is not the one killed"
else
  fail "the scheduler has no leader after its leader was killed"
fi
check "orders failed" "$(orders_in failed)" "$ORDERS"
check "orders whose reason is an expired hold" \
  "$(sql orders "SELECT count(*) FROM orders WHERE drop_id = '$DROP' AND failure_reason = 'hold expired'")" "$ORDERS"
check "holds released" "$(holds_in released)" "$ORDERS"
check "tickets back in stock" "$(available)" "$CAPACITY"
check "tickets issued" "$(tickets)" 0
check "payments dead-lettered during the outage" "$(($(dead_letters payments) - dead_before))" "$ORDERS"
wait_for 30 notified_at_least $((notified_before + ORDERS)) || true
check "customers told that their order failed" "$(($(notified) - notified_before))" "$ORDERS"

# --- the provider recovers, and the old payments are retried -------------------

echo
compose up -d --wait payments inventory scheduler >/dev/null 2>&1
wait_for 30 ready payments && wait_for 30 ready inventory ||
  { echo "payments and inventory did not come back with the normal settings" >&2; exit 2; }
echo "  provider restored"

others_before=$(dead_letters "inventory fulfillment orders notifications")
before=$(state)
moved=$(redrive payments)
echo "  moved $moved dead-lettered payments back onto the payments queue"
wait_for 60 refused_at_least "$ORDERS" || true
# Time for the refusals to reach inventory and orders, where they must change nothing.
sleep 5
echo

check "payments put back on the queue" "$moved" "$ORDERS"
check "payments refused without a charge, because the hold had expired" \
  "$(sql payments "SELECT count(*) FROM payments p JOIN outbox o ON o.payload->'data'->>'order_id' = p.order_id::text WHERE p.status = 'expired' AND o.payload->'data'->>'drop_id' = '$DROP'")" "$ORDERS"
check "payments charged after the hold had expired" \
  "$(sql payments "SELECT count(*) FROM payments p JOIN outbox o ON o.payload->'data'->>'order_id' = p.order_id::text WHERE p.status = 'succeeded' AND o.payload->'data'->>'drop_id' = '$DROP'")" 0
# The payments count is the one thing that should have moved.
after=$(state)
if [ "${before/payments\[0\]/payments[$ORDERS]}" = "$after" ]; then
  pass "orders, holds, stock and tickets are unchanged: $after"
else
  fail "state changed when the old payments were retried: before $before, after $after"
fi
check "customers told a second time" "$(($(notified) - notified_before - ORDERS))" 0
check "messages dead-lettered by the retry" "$(($(dead_letters "inventory fulfillment orders notifications") - others_before))" 0
check "scheduler replicas running again" "$(schedulers | wc -l)" 2

verdict "outage check"

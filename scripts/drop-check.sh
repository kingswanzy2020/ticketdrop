#!/usr/bin/env bash
# Runs a drop against the local stack (`make up`) and proves:
#
#   1. a drop cannot be ordered before its opening time, and goes on sale
#      without anyone touching it
#   2. no ticket is oversold: more tickets are ordered than exist, and the
#      tickets issued never exceed the capacity
#   3. every accepted order ends ticketed or failed, and the stock adds up
#   4. killing a service mid-run loses no order and duplicates nothing
#   5. every customer is told how their order ended, once
#   6. an order can be placed through the shopfront, which then shows how it ended
#   7. an event delivered a second time changes nothing, at every hop
#   8. a message that can never be handled reaches the dead-letter queue
#
# Settings, as environment variables:
#   ORDERS    orders placed (default 600; each asks for 1 to 4 tickets)
#   CAPACITY  tickets in the drop (default 1000)
#   KILL      service killed mid-run: payments (default), inventory or fulfillment
set -euo pipefail

cd "$(dirname "$0")/.."
source scripts/lib.sh

ORDERS=${ORDERS:-600}
CAPACITY=${CAPACITY:-1000}
KILL=${KILL:-payments}
# A drop of its own for each run, so earlier runs do not affect the counts.
DROP=check-$(date +%s)
TIER=general
export GATEWAY DROP TIER
export -f place

# handled <service>: how many of this drop's orders that consumer has dealt
# with, read from what it wrote to its own database.
handled() {
  case $1 in
    payments) payment_outcomes ;;
    inventory) sql inventory "SELECT count(*) FROM holds WHERE drop_id = '$DROP' AND status <> 'held'" ;;
    fulfillment) sql fulfillment "SELECT count(DISTINCT order_id) FROM tickets WHERE drop_id = '$DROP'" ;;
  esac
}

# replay <publisher> <event type> <order status> <consumer...>: publishes again
# an event the publisher has already sent, for one of this drop's orders in
# that status.
replay() {
  local publisher=$1 type=$2 status=$3 order payload
  shift 3
  order=$(sql orders "SELECT id FROM orders WHERE drop_id = '$DROP' AND status = '$status' ORDER BY id LIMIT 1")
  if [ -z "$order" ]; then
    echo "  SKIP  no $status order in this run, so no $type to replay"
    return
  fi
  payload=$(sql "$publisher" "SELECT payload FROM outbox WHERE event_type = '$type' AND payload->'data'->>'order_id' = '$order'")
  if [ -z "$payload" ]; then
    fail "$publisher has no $type event for $status order $order"
    return
  fi
  publish_again "$type" "$payload" "$@"
}

case $KILL in
  payments | inventory | fulfillment) ;;
  *) echo "KILL must be payments, inventory or fulfillment" >&2; exit 2 ;;
esac
require_stack

echo "drop check: $ORDERS orders for $CAPACITY tickets in drop $DROP, killing $KILL mid-run"
echo

# --- the drop is announced, then opens on its own -----------------------------

opens_at=$(($(date +%s) + 3))
create_drop "$CAPACITY" "$opens_at"
check "an order placed before the opening time is refused (HTTP status)" "$(place 1)" 404

# Nothing here opens the drop. The scheduler tells catalog when the time has
# come, catalog publishes drop.opened, and inventory creates the stock.
until on_sale || [ "$(date +%s)" -gt $((opens_at + 30)) ]; do sleep 0.1; done
if on_sale; then
  pass "the drop went on sale $(($(date +%s%3N) - opens_at * 1000))ms after its opening time"
else
  fail "the drop was not on sale 30s after its opening time"
  verdict "drop check"
fi

# --- one order through the shopfront ------------------------------------------

# The shopfront is one more client of the public API. One order goes through
# its form, sent the way a browser without JavaScript would send it.
notified_before=$(notified)
page=$(curl -fsS "$WEB/drops/$DROP" || true)
if grep -q "Check $DROP" <<<"$page" && grep -q '<form' <<<"$page"; then
  pass "the shopfront shows the drop and its order form"
else
  fail "the shopfront does not show the drop and its order form"
fi
shop_order=$(curl -s -o /dev/null -w '%{redirect_url}' -X POST "$WEB/drops/$DROP/orders" \
  --data-urlencode "tier=$TIER" --data-urlencode "quantity=2" --data-urlencode "email=shopper@example.com")
shop_order=${shop_order##*/}
if [[ $shop_order =~ ^[0-9a-f-]{36}$ ]]; then
  pass "an order placed through the shopfront was accepted"
  shop_orders=1
else
  fail "an order placed through the shopfront was not accepted"
  shop_orders=0
fi

# --- the drop -----------------------------------------------------------------

dead_before=$(dead_letters)
started=$SECONDS
statuses=$(seq "$ORDERS" | xargs -P 16 -I{} bash -c 'place {}' | sort | uniq -c)
burst=$(awk '$2 == 201 { n = $1 } END { print n + 0 }' <<<"$statuses")
sold_out=$(awk '$2 == 409 { n = $1 } END { print n + 0 }' <<<"$statuses")
accepted=$((burst + shop_orders))
echo
echo "  placed $ORDERS orders in $((SECONDS - started))s: $burst accepted, $sold_out refused as sold out"

# SIGKILL, not SIGTERM: no graceful shutdown, as when a node disappears. The
# messages the service was holding come back when their visibility timeout
# expires.
wait_for 60 finished_at_least $((accepted / 10)) || true
compose kill -s SIGKILL "$KILL" >/dev/null 2>&1
at_kill=$(handled "$KILL")
echo "  killed $KILL after it had handled $at_kill orders"
sleep 2

compose start "$KILL" >/dev/null 2>&1
restarted=$SECONDS
if wait_for 240 finished_at_least "$accepted"; then
  echo "  restarted $KILL; every order finished $((SECONDS - restarted))s later"
else
  echo "  restarted $KILL; still waiting after $((SECONDS - restarted))s"
fi
echo

ticketed=$(orders_in ticketed)
failed=$(orders_in failed)
issued=$(tickets)

check "responses that were neither accepted nor sold out" "$((ORDERS - burst - sold_out))" 0
if [ "$sold_out" -gt 0 ]; then
  pass "demand exceeded capacity ($sold_out orders refused as sold out)"
else
  fail "nothing sold out, so overselling was never at risk; lower CAPACITY or raise ORDERS"
fi
in_the_end=$(handled "$KILL")
if [ "$at_kill" -lt "$in_the_end" ]; then
  pass "$KILL was killed mid-run ($at_kill of $in_the_end orders handled)"
else
  fail "$KILL had no work left when it was killed; rerun with more orders"
fi
check "accepted orders that finished (ticketed $ticketed + failed $failed)" "$((ticketed + failed))" "$accepted"

if [ "$issued" -le "$CAPACITY" ]; then
  pass "tickets issued within capacity ($issued of $CAPACITY)"
else
  fail "OVERSOLD: $issued tickets issued for a capacity of $CAPACITY"
fi

# Each ticketed order must hold exactly its quantity, and no other order may
# hold a ticket at all. Comparing totals could hide two opposite mistakes.
wrong=$(diff \
  <(sql orders "SELECT id, quantity FROM orders WHERE drop_id = '$DROP' AND status = 'ticketed' ORDER BY id") \
  <(sql fulfillment "SELECT order_id, count(*) FROM tickets WHERE drop_id = '$DROP' GROUP BY order_id ORDER BY order_id") |
  grep -c '^[<>]' || true)
check "orders with the wrong number of tickets" "$wrong" 0

# Stock must account for every ticket: what left it and did not come back is
# exactly what was issued.
check "tickets gone from stock equals tickets issued" "$((CAPACITY - $(available)))" "$issued"
check "holds still waiting for a payment" "$(holds_in held)" 0
check "holds released equals orders failed" "$(holds_in released)" "$failed"
check "messages dead-lettered during the run" "$(($(dead_letters) - dead_before))" 0

# One notice for each order that ended, whichever way it ended.
wait_for 30 notified_at_least $((notified_before + accepted)) || true
check "customers told how their order ended, once each" "$(($(notified) - notified_before))" "$accepted"

if [ "$shop_orders" = 1 ]; then
  outcome=$(sql orders "SELECT status FROM orders WHERE id = '$shop_order'")
  case $outcome in
    ticketed) words="You're in" ;;
    failed) words="did not go through" ;;
    *) words="(the order never finished)" ;;
  esac
  if curl -fsS "$WEB/orders/$shop_order" | grep -q "$words"; then
    pass "the shopfront shows its order as $outcome"
  else
    fail "the shopfront does not show its order as $outcome"
  fi
fi

# --- the same events, delivered again -----------------------------------------

echo
before=$(state)
publish_again drop.opened \
  "$(sql catalog "SELECT payload FROM outbox WHERE event_type = 'drop.opened' AND payload->'data'->>'drop_id' = '$DROP'")" \
  inventory
replay orders order.created ticketed payments
replay payments payment.succeeded ticketed inventory
replay payments payment.failed failed inventory orders
replay inventory order.confirmed ticketed fulfillment
replay fulfillment ticket.issued ticketed orders
after=$(state)
if [ "$before" = "$after" ]; then
  pass "nothing changed after the replays: $after"
else
  fail "state changed after the replays: before $before, after $after"
fi

# --- a message no retry can fix -----------------------------------------------

echo
dead_before=$(dead_letters payments)
started=$SECONDS
bus sqs send-message --queue-url "$QUEUES/payments-events" --message-body 'this is not an event' >/dev/null

if wait_for 120 dead_letters_above payments "$dead_before"; then
  pass "poison message reached the dead-letter queue after $((SECONDS - started))s"
else
  fail "poison message did not reach the dead-letter queue within 120s"
fi

verdict "drop check"

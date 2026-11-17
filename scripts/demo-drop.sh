#!/usr/bin/env bash
# Sends a crowd at a drop with k6 (loadtest/drop.js), then checks in the
# databases that nothing was oversold and nothing was left unfinished.
#
# Where drop-check sends 16 orders at a time and breaks things, this sends many
# customers who all wait for the opening and then buy within a few seconds of
# each other, through the shopfront, and measures how the system answers.
#
# k6 runs from its Docker image unless a k6 binary is installed.
#
# Settings, as environment variables:
#   CUSTOMERS  customers in the crowd (default 1000)
#   CAPACITY   tickets in the drop (default 1000; customers buy 1 to 4 each)
#   ARRIVE     seconds over which the crowd arrives before the opening (default 15)
#   TARGET     compose (default) or cluster, as for drop-check
set -euo pipefail

cd "$(dirname "$0")/.."
source scripts/lib.sh

CUSTOMERS=${CUSTOMERS:-1000}
CAPACITY=${CAPACITY:-1000}
ARRIVE=${ARRIVE:-15}
K6_IMAGE=grafana/k6:2.4.0
DROP=load-$(date +%s)
TIER=general
RESULTS=loadtest/results

none_pending() { [ "$(orders_in pending)" -eq 0 ]; }

# k6 <args...>: runs k6, with BASE set to where k6 itself can reach the shopfront.
k6_run() {
  if command -v k6 >/dev/null; then
    BASE=$WEB k6 "$@"
    return
  fi
  local base network=()
  if [ "${TARGET:-compose}" = cluster ]; then
    # Traefik's port on this machine, seen from inside a container.
    base=http://host.docker.internal:8088
  else
    # Straight to web on the compose network.
    base=http://web:8080
    network=(--network ticketdrop_default)
  fi
  docker run --rm -i "${network[@]}" -u "$(id -u):$(id -g)" \
    -v "$PWD/loadtest:/loadtest" -w / -e BASE="$base" \
    -e DROP -e TIER -e CUSTOMERS -e ARRIVE \
    "$K6_IMAGE" "$@"
}

# result <metric> <field>: one number from k6's summary of this run.
result() { jq -r --arg m "$1" --arg f "$2" '.metrics[$m][$f] // .metrics[$m].values[$f] // 0' "$RESULTS/$DROP.json"; }

require_stack
mkdir -p "$RESULTS"
export DROP TIER CUSTOMERS ARRIVE

echo "demo drop: $CUSTOMERS customers for $CAPACITY tickets in drop $DROP (${TARGET:-compose})"
echo

# The drop opens a few seconds after the last customer has arrived, so the
# whole crowd is waiting on the page when it does.
opens_at=$(($(date +%s) + ${ARRIVE%.*} + 5))
create_drop "$CAPACITY" "$opens_at"
dead_before=$(dead_letters)
echo "  drop opens at $(date -d "@$opens_at" +%T); k6 log in $RESULTS/$DROP.log"

started=$SECONDS
k6_status=0
k6_run run --quiet --summary-export "/$RESULTS/$DROP.json" /loadtest/drop.js >"$RESULTS/$DROP.log" 2>&1 || k6_status=$?
echo "  the crowd was done $((SECONDS - started))s after it started arriving (k6 exit code $k6_status)"
if [ ! -s "$RESULTS/$DROP.json" ]; then
  echo "k6 wrote no summary; see $RESULTS/$DROP.log" >&2
  tail -n 20 "$RESULTS/$DROP.log" >&2
  exit 2
fi

# Customers stop watching after two minutes; give any stragglers a little more.
wait_for 120 none_pending || true

accepted=$(result buy_accepted count)
sold_out=$(result buy_sold_out count)
issued=$(tickets)
ticketed=$(orders_in ticketed)
failed=$(orders_in failed)

echo
echo "  buys:      $accepted accepted, $sold_out sold out, $(result buy_refused_other count) refused otherwise, $(result buy_unanswered count) unanswered"
printf '  buy time:  median %.0f ms, p95 %.0f ms, p99 %.0f ms, max %.0f ms\n' \
  "$(result buy_time med)" "$(result buy_time 'p(95)')" "$(result buy_time 'p(99)')" "$(result buy_time max)"
printf '  settle:    median %.1f s, p95 %.1f s, max %.1f s from the answer to the order'"'"'s end\n' \
  "$(jq -n "$(result settle_time med)/1000")" "$(jq -n "$(result settle_time 'p(95)')/1000")" "$(jq -n "$(result settle_time max)/1000")"
printf '  requests:  %d in all, %.0f a second on average, %.2f%% failed\n' \
  "$(result http_reqs count)" "$(result http_reqs rate)" "$(jq -n "$(result http_req_failed rate)*100")"
echo

check "k6 thresholds (every customer answered, every order ended in view)" "$k6_status" 0
if [ "$sold_out" -gt 0 ]; then
  pass "demand exceeded capacity ($sold_out customers told sold out)"
else
  fail "nothing sold out, so overselling was never at risk; lower CAPACITY or raise CUSTOMERS"
fi
check "orders in the database equal the buys k6 saw accepted" "$(sql orders "SELECT count(*) FROM orders WHERE drop_id = '$DROP'")" "$accepted"
check "orders still pending" "$(orders_in pending)" 0
if [ "$issued" -le "$CAPACITY" ]; then
  pass "tickets issued within capacity ($issued of $CAPACITY)"
else
  fail "OVERSOLD: $issued tickets issued for a capacity of $CAPACITY"
fi
wrong=$(diff \
  <(sql orders "SELECT id, quantity FROM orders WHERE drop_id = '$DROP' AND status = 'ticketed' ORDER BY id") \
  <(sql fulfillment "SELECT order_id, count(*) FROM tickets WHERE drop_id = '$DROP' GROUP BY order_id ORDER BY order_id") |
  grep -c '^[<>]' || true)
check "orders with the wrong number of tickets" "$wrong" 0
check "tickets gone from stock equals tickets issued" "$((CAPACITY - $(available)))" "$issued"
check "holds released equals orders failed (ticketed $ticketed, failed $failed)" "$(holds_in released)" "$failed"
check "messages dead-lettered during the run" "$(($(dead_letters) - dead_before))" 0

verdict "demo drop"

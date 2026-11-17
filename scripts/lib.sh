# Shared by the check scripts in this directory. Source it; do not run it.
# It expects the caller to have set DROP and TIER before using the helpers
# that read a drop's state.
#
# TARGET=compose (the default) talks to the stack from `make up`. TARGET=cluster
# talks to the Kubernetes cluster instead: the public API through the Gateway on
# localhost:8088, and catalog and goaws through port-forwards. The cluster
# versions of the helpers are at the end of this file.

WEB=http://localhost:8092
GATEWAY=http://localhost:8090
CATALOG=http://localhost:8091
BUS=http://localhost:4100
QUEUES=$BUS/000000000000
TOPIC=arn:aws:sns:us-east-1:000000000000:ticketdrop-events
CONSUMERS="payments inventory fulfillment orders notifications"

# admin_port <service>: where that service serves /readyz and /metrics.
admin_port() {
  case $1 in
    gateway) echo 9190 ;;
    orders) echo 9191 ;;
    fulfillment) echo 9192 ;;
    payments) echo 9193 ;;
    inventory) echo 9194 ;;
    catalog) echo 9195 ;;
    web) echo 9198 ;;
    notifications) echo 9199 ;;
    *) echo "unknown service: $1" >&2; exit 2 ;;
  esac
}

compose() { docker compose -f deploy/local/compose.yaml "$@"; }

# sql <database> <query>: the result as bare rows.
sql() { compose exec -T postgres psql -U postgres -d "$1" -AtX -v ON_ERROR_STOP=1 -c "$2"; }

# The emulator ignores credentials, but the CLI refuses to run without them.
# --endpoint-url on every call is what keeps these scripts off the real account.
bus() {
  AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local AWS_DEFAULT_REGION=us-east-1 AWS_PAGER="" \
    aws --endpoint-url "$BUS" --output json "$@"
}

failures=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# check <description> <actual> <expected>
check() {
  if [ "$2" = "$3" ]; then pass "$1 ($2)"; else fail "$1: got $2, want $3"; fi
}

# verdict <name>: prints the result of the whole check and exits accordingly.
verdict() {
  echo
  if [ "$failures" -gt 0 ]; then
    echo "$1 FAILED: $failures check(s)"
    exit 1
  fi
  echo "$1 passed"
}

# wait_for <seconds> <command...>: retries once a second until the command succeeds.
wait_for() {
  local deadline=$((SECONDS + $1))
  shift
  until "$@"; do
    [ "$SECONDS" -lt "$deadline" ] || return 1
    sleep 1
  done
}

# ready <service>: whether that service answers its readiness probe.
ready() { curl -fsS -o /dev/null --max-time 3 "http://localhost:$(admin_port "$1")/readyz"; }

# require_stack: stops the script unless the tools and the whole stack are there.
require_stack() {
  local tool service
  for tool in docker curl jq aws; do
    command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
  done
  for service in web gateway catalog orders inventory payments fulfillment notifications; do
    ready "$service" || { echo "$service is not ready; run 'make up' first" >&2; exit 2; }
  done
  [ "$(leaders | wc -l)" -eq 1 ] || { echo "the scheduler has no leader; run 'make up' first" >&2; exit 2; }
}

# kill_service <service>: stops it with no graceful shutdown, as when a node
# disappears. restart_service <service>: brings it back.
kill_service() { compose kill -s SIGKILL "$1" >/dev/null 2>&1; }
restart_service() { compose start "$1" >/dev/null 2>&1; }

# --- the scheduler ---------------------------------------------------------------

# schedulers: the scheduler containers that are running.
schedulers() { compose ps -q scheduler; }

# leads <container>: whether that scheduler replica reports itself the leader.
leads() {
  local port
  port=$(docker port "$1" 9090/tcp 2>/dev/null | head -n1 | sed 's/.*://')
  [ -n "$port" ] &&
    [ "$(curl -fsS --max-time 2 "http://localhost:$port/metrics" 2>/dev/null | awk '$1 == "scheduler_leader" { print $2 }')" = 1 ]
}

# leaders: the scheduler containers that report themselves the leader. There
# should be exactly one.
leaders() {
  local container
  for container in $(schedulers); do
    if leads "$container"; then echo "$container"; fi
  done
}

# --- one drop --------------------------------------------------------------------

# create_drop <capacity> <opening time, in seconds since 1970>: announces
# $DROP with one tier, $TIER.
create_drop() {
  curl -fsS -o /dev/null -X PUT "$CATALOG/internal/v1/drops/$DROP" -H 'Content-Type: application/json' \
    -d "{\"name\":\"Check $DROP\",\"venue\":\"localhost\",\"opens_at\":\"$(date -u -d "@$2" +%Y-%m-%dT%H:%M:%SZ)\",\"tiers\":[{\"tier\":\"$TIER\",\"price_cents\":2500,\"capacity\":$1}]}"
}

# on_sale: whether $DROP has stock, which is what lets it be ordered.
on_sale() { curl -fsS -o /dev/null --max-time 3 "$GATEWAY/v1/drops/$DROP/availability" 2>/dev/null; }

# place <n>: one order for 1 to 4 tickets of $DROP; prints the HTTP status.
place() {
  curl -s -o /dev/null -w '%{http_code}\n' --max-time 10 -X POST "$GATEWAY/v1/orders" \
    -H 'Content-Type: application/json' \
    -d "{\"drop_id\":\"$DROP\",\"tier\":\"$TIER\",\"quantity\":$(($1 % 4 + 1)),\"customer_email\":\"buyer$1@example.com\"}"
}

# orders_in <status>: this drop's orders in that status.
orders_in() { sql orders "SELECT count(*) FROM orders WHERE drop_id = '$DROP' AND status = '$1'"; }
finished() { sql orders "SELECT count(*) FROM orders WHERE drop_id = '$DROP' AND status <> 'pending'"; }
finished_at_least() { [ "$(finished)" -ge "$1" ]; }
tickets() { sql fulfillment "SELECT count(*) FROM tickets WHERE drop_id = '$DROP'"; }
available() { sql inventory "SELECT available FROM stock WHERE drop_id = '$DROP' AND tier = '$TIER'"; }
holds_in() { sql inventory "SELECT count(*) FROM holds WHERE drop_id = '$DROP' AND status = '$1'"; }

# payment_outcomes: how many of this drop's orders payments has dealt with.
payment_outcomes() { sql payments "SELECT count(*) FROM outbox WHERE payload->'data'->>'drop_id' = '$DROP'"; }

# state: everything about this drop that a stray or repeated event could
# wrongly change, as one line. The last item is every notice sent so far, for
# any drop: a repeated event must not tell a customer anything twice.
state() {
  echo "orders[$(sql orders "SELECT string_agg(status || '=' || n, ' ' ORDER BY status) FROM (SELECT status, count(*) n FROM orders WHERE drop_id = '$DROP' GROUP BY status) s")]" \
    "payments[$(payment_outcomes)]" \
    "holds[$(sql inventory "SELECT string_agg(status || '=' || n, ' ' ORDER BY status) FROM (SELECT status, count(*) n FROM holds WHERE drop_id = '$DROP' GROUP BY status) s")]" \
    "available[$(available)]" \
    "tickets[$(tickets)]" \
    "notices[$(notified)]"
}

# notified: notifications sent since the notifications service started, from
# its own metrics.
notified() {
  curl -fsS "http://localhost:$(admin_port notifications)/metrics" |
    awk '/^notifications_sent_total\{/ { n += $NF } END { print n + 0 }'
}
notified_at_least() { [ "$(notified)" -ge "$1" ]; }

# --- the bus ---------------------------------------------------------------------

# dead_letters [service]: messages in that consumer's dead-letter queue, or in
# all of them.
dead_letters() {
  local total=0 service
  for service in ${1:-$CONSUMERS}; do
    total=$((total + $(bus sqs get-queue-attributes --queue-url "$QUEUES/$service-events-dlq" --attribute-names All |
      jq -r '.Attributes.ApproximateNumberOfMessages')))
  done
  echo "$total"
}
dead_letters_above() { [ "$(dead_letters "$1")" -gt "$2" ]; }

# duplicates <service> <event type>: deliveries of that type the service
# recognised as already handled, from its own metrics.
duplicates() {
  curl -fsS "http://localhost:$(admin_port "$1")/metrics" |
    awk -v type="$2" 'index($0, "events_consumed_total{event_type=\"" type "\",result=\"duplicate\"}") == 1 { n = $NF } END { print n + 0 }'
}
duplicates_above() { [ "$(duplicates "$1" "$2")" -gt "$3" ]; }

# publish_again <event type> <payload> <consumer...>: publishes an event that
# has been published before, and waits for each consumer to recognise it.
publish_again() {
  local type=$1 payload=$2 consumer
  shift 2
  local -A before
  for consumer in "$@"; do before[$consumer]=$(duplicates "$consumer" "$type"); done
  bus sns publish --topic-arn "$TOPIC" --message "$payload" \
    --message-attributes "{\"event_type\":{\"DataType\":\"String\",\"StringValue\":\"$type\"}}" >/dev/null
  for consumer in "$@"; do
    if wait_for 30 duplicates_above "$consumer" "$type" "${before[$consumer]}"; then
      pass "replayed $type recognised as a duplicate by $consumer"
    else
      fail "replayed $type was not counted as a duplicate by $consumer within 30s"
    fi
  done
}

# redrive <service>: moves the messages about $DROP from that consumer's
# dead-letter queue back to its queue, and prints how many it moved. Any other
# message is left where it is.
redrive() {
  local service=$1 moved=0 attempt batch i body handle
  for attempt in 1 2 3 4 5; do
    batch=$(bus sqs receive-message --queue-url "$QUEUES/$service-events-dlq" --max-number-of-messages 10)
    [ -n "$batch" ] || break
    for i in $(jq -r '.Messages // [] | keys[]' <<<"$batch"); do
      body=$(jq -r ".Messages[$i].Body" <<<"$batch")
      handle=$(jq -r ".Messages[$i].ReceiptHandle" <<<"$batch")
      if jq -e --arg drop "$DROP" '.data.drop_id == $drop' <<<"$body" >/dev/null 2>&1; then
        bus sqs send-message --queue-url "$QUEUES/$service-events" --message-body "$body" >/dev/null
        bus sqs delete-message --queue-url "$QUEUES/$service-events-dlq" --receipt-handle "$handle"
        moved=$((moved + 1))
      fi
    done
  done
  echo "$moved"
}

# --- TARGET=cluster ----------------------------------------------------------------

if [ "${TARGET:-compose}" = cluster ]; then
  NS=ticketdrop
  # Traefik's public port: / goes to web, /v1 to the gateway.
  WEB=http://localhost:8088
  GATEWAY=http://localhost:8088
  # Catalog's internal API and the bus are not on the Gateway, so each run
  # forwards a local port to them and stops the forward when it exits.
  CATALOG=http://localhost:${CATALOG_PORT:-18091}
  BUS=http://localhost:${BUS_PORT:-14100}
  QUEUES=$BUS/000000000000

  forwards=()
  trap '[ ${#forwards[@]} -eq 0 ] || kill "${forwards[@]}" 2>/dev/null || true' EXIT

  # forward <namespace> <service> <local url> <port>: a port-forward for this run.
  forward() {
    local port=${3##*:}
    kubectl port-forward -n "$1" "svc/$2" "$port:$4" >/dev/null 2>&1 &
    forwards+=($!)
    wait_for 10 curl -s -o /dev/null --max-time 1 "$3/" ||
      { echo "the port-forward to $2 on $port did not come up" >&2; exit 2; }
  }

  sql() { kubectl exec -n data pg-1 -c postgres -- psql -U postgres -d "$1" -AtX -v ON_ERROR_STOP=1 -c "$2"; }

  # pods <service>: its running pods.
  pods() {
    kubectl get pods -n "$NS" -l "app.kubernetes.io/name=$1" --field-selector=status.phase=Running \
      -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
  }

  # metrics <service>: /metrics of every pod of that service, read through the
  # API server. A pod's counters start again at 0 when the pod is replaced.
  metrics() {
    local pod
    for pod in $(pods "$1"); do
      kubectl get --raw "/api/v1/namespaces/$NS/pods/$pod:9090/proxy/metrics" 2>/dev/null || true
    done
  }

  notified() { metrics notifications | awk '/^notifications_sent_total\{/ { n += $NF } END { print n + 0 }'; }

  duplicates() {
    metrics "$1" |
      awk -v type="$2" 'index($0, "events_consumed_total{event_type=\"" type "\",result=\"duplicate\"}") == 1 { n += $NF } END { print n + 0 }'
  }

  # ready <service>: whether at least one of its pods is Ready.
  ready() { [ "$(kubectl get deploy -n "$NS" "$1" -o jsonpath='{.status.readyReplicas}' 2>/dev/null)" -ge 1 ] 2>/dev/null; }

  schedulers() { pods scheduler; }
  leads() {
    [ "$(kubectl get --raw "/api/v1/namespaces/$NS/pods/$1:9090/proxy/metrics" 2>/dev/null | awk '$1 == "scheduler_leader" { print $2 }')" = 1 ]
  }

  # fulfillment is not required to be Ready: KEDA keeps it at 0 pods while its
  # queue is empty.
  require_stack() {
    local tool service
    for tool in kubectl curl jq aws; do
      command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
    done
    for service in web gateway catalog orders inventory payments notifications; do
      ready "$service" || { echo "$service has no Ready pod in $NS" >&2; exit 2; }
    done
    kubectl get deploy -n "$NS" fulfillment >/dev/null || exit 2
    [ "$(leaders | wc -l)" -eq 1 ] || { echo "the scheduler has no leader" >&2; exit 2; }
    curl -s -o /dev/null --max-time 3 "$GATEWAY/" || { echo "nothing answers on $GATEWAY" >&2; exit 2; }
    forward "$NS" catalog "$CATALOG" 8080
    forward data goaws "$BUS" 4100
  }

  # Deleting a pod with no grace period is as close to SIGKILL as Kubernetes
  # gets. Its Deployment starts a replacement, so there is nothing to restart.
  kill_service() {
    local pod
    pod=$(pods "$1" | head -n1)
    if [ -z "$pod" ]; then
      fail "there was no running $1 pod to delete"
      return
    fi
    echo "  $(date -Is) \$ kubectl delete pod -n $NS $pod --grace-period=0 --force"
    kubectl delete pod -n "$NS" "$pod" --grace-period=0 --force >/dev/null 2>&1
  }
  restart_service() { :; }
fi

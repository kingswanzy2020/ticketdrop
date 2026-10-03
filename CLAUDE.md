# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

- `make test` — `go test ./...` (unit tests only; none need Postgres or the emulator)
- Single test: `go test ./pkg/platform/events -run TestReserve`
- `make vet` — `go vet` plus a `gofmt -l` check that fails on unformatted files
- `make up` / `make down` — build and start / destroy the local stack (`down` deletes its volumes). The shopfront is on `http://localhost:8092`; the gateway (public API) on 8090; catalog's internal API on 8091; admin ports (`/readyz`, `/metrics`) are 9190 gateway, 9191 orders, 9192 fulfillment, 9193 payments, 9194 inventory, 9195 catalog, 9196-9197 the two scheduler replicas, 9198 web, 9199 notifications; the emulator is on 4100.
- `make logs`, `make ps` — follow logs / show containers
- `make drop-check` (optionally `ORDERS=600 CAPACITY=1000 KILL=payments|inventory|fulfillment`) — end-to-end proof against a running stack (needs `docker`, `curl`, `jq`, `aws`): announces a drop and waits for it to open on its own, places more orders than it has tickets, SIGKILLs one service mid-run, then asserts nothing was oversold, every accepted order ended `ticketed` or `failed`, stock adds up, each customer was notified once, an order placed through the shopfront shows its outcome there, replayed events change nothing at any hop, and a poison message reaches the DLQ. Run it once per `KILL` value after changing anything on the event path.
- `make outage-check` — about two minutes. Restarts payments with the provider down and inventory with 40-second holds, kills the scheduler's leader, then asserts the standby took over, the unpayable orders failed when their holds expired, the tickets returned to stock, and retrying the dead-lettered payments afterwards charges nobody. It restores the normal settings on exit. Run it after changing holds, payments or the scheduler.
- `scripts/lib.sh` holds the helpers both checks share (ports, SQL, the emulator CLI wrapper, replay and redrive).
- A drop must exist and be open before it can be ordered: `curl -X PUT localhost:8091/internal/v1/drops/<drop> -d '{"name":"...","venue":"...","opens_at":"2027-06-01T10:00:00Z","tiers":[{"tier":"general","price_cents":2500,"capacity":100}]}'`. The scheduler opens it at `opens_at`; a time in the past opens it within a second.

## Architecture

One Go module (`github.com/kingswanzy2020/ticketdrop`). Nine services under `services/<name>/{cmd,internal,migrations}` share a platform library in `pkg/platform`. A service without a database has no `migrations`.

- **web** — the shopfront (`internal/shop`). Server-rendered Go templates plus htmx 2.0.11, both embedded in the binary; fragments in `templates/fragments.html` are also served alone for htmx to swap in. It reads everything through the gateway's public API (`API_URL`) and must not be given a private route into a service. htmx discards error responses, so a message meant for the customer goes back to htmx with status 200 (`Shop.notice`).
- **gateway** — reverse proxy; only explicitly listed routes are forwarded (`internal/proxy`). Routes under `/internal/` exist on services for other services and must never be listed here.
- **catalog** — owns a drop's name, venue, opening time, tiers, prices and capacities. Publishes `drop.opened` when a drop opens, and refuses changes to a drop after that. Publishes only: it has a relay and no queue.
- **orders** — `POST /v1/orders` first calls inventory over HTTP to hold the tickets (409 when sold out, 404 when the drop is not on sale, 503 when inventory cannot be reached), then writes the order and an `order.created` outbox row in one transaction. An order ends `ticketed` (on `ticket.issued`) or `failed` (on `payment.failed` or `hold.expired`, with a `failure_reason`), and only from `pending`. In the same transaction it publishes `order.ticketed` or `order.failed`, the one event that says how the order turned out.
- **inventory** — owns stock, which it creates from `drop.opened` and from nothing else. A hold is one conditional `UPDATE stock ... WHERE available >= n`, which is what prevents overselling; do not replace it with a read followed by a write. Consumes `payment.succeeded` (confirms the hold, emits `order.confirmed`) and `payment.failed` (returns the tickets to stock). Releases unsettled holds `HOLD_GRACE` after they expire, emitting `hold.expired`.
- **payments** — consumes `order.created`, calls a simulated provider (`PAYMENT_LATENCY`, `PAYMENT_DECLINE_RATE`, `PAYMENT_ERROR_RATE`), emits `payment.succeeded` or `payment.failed`. A decline is an outcome; a provider error is retried and then dead-lettered. It never charges an order whose hold has expired (`hold_expires_at` in the event).
- **fulfillment** — consumes `order.confirmed`, issues tickets, emits `ticket.issued`. `SIMULATED_RENDER_TIME` and `CONSUMER_CONCURRENCY` deliberately throttle it so a backlog is observable.
- **notifications** — consumes `order.ticketed` and `order.failed` and sends the customer a message (a log line for now, behind the `Sender` interface). It has no database and is the one consumer that does not use `events.Once`: a redelivered event is sent twice, on purpose (DECISIONS entry 23). Do not copy that into a consumer whose work must not repeat.
- **scheduler** — a clock. Each second it POSTs to catalog's `open-due` endpoint and every five seconds to inventory's `expire` endpoint; the services do the work. Runs as two replicas; the one holding a Postgres advisory lock leads (`scheduler_leader` gauge). Its database has no tables and it has no migrations. Anything it calls must be safe to call twice at once.

Known and not handled automatically: a `payment.succeeded` that reaches inventory after its hold was released fails into the dead-letter queue, for a person to refund (DECISIONS entry 21).

### The event path (spans several files)

Each service has its own Postgres database. Apart from the hold call from orders to inventory and the scheduler's two timer calls, services talk only through events: **outbox table → relay → SNS topic → per-consumer SQS queue (filtered on the `event_type` message attribute) → consumer → `events.Once`**. Which queue receives which event type is set in `deploy/local/goaws.yaml`; a new event type or consumer needs a change there as well as in `pkg/contracts`.

- `pkg/contracts` is the wire agreement: add fields, never rename or remove. Event type strings double as SNS filter-policy values.
- `events.Enqueue(tx, e)` writes to the outbox inside the caller's transaction. `events.Relay` publishes with `FOR UPDATE SKIP LOCKED` (safe with multiple replicas) and is at-least-once.
- `events.Consumer` handlers must be idempotent (notifications excepted, see above). Wrap work in `events.Once`, which records the event ID in `processed_events` in the same transaction as the work; a redelivery returns `ErrDuplicate`, which is acked and counted as `result="duplicate"`.
- A handler error leaves the message on the queue for redelivery. An unparseable message is also left, and SQS dead-letters it after `maxReceiveCount` (3). Keep `CONSUMER_TIMEOUT` below the queue's visibility timeout (10s locally, set in `deploy/local/goaws.yaml`).
- `events.Schema()` (`pkg/platform/events/schema`) holds the outbox/dedupe tables; every publishing or consuming service migrates it alongside its own `migrations/` via `db.Migrate`, which runs at startup under a Postgres advisory lock so replicas can start together.

### Service lifecycle

Each `cmd/main.go` calls `service.Main(name, run)` and then `s.Serve(ctx, appHandler, workers...)` (`pkg/platform/service`). `Serve` runs an admin server (health, Prometheus) plus the app server and workers, and shuts down in a fixed order (readiness false → `SHUTDOWN_DELAY` → drain HTTP → stop workers one at a time, last registered first → stop admin). Register the outbox relay before the consumer: it then stops last and makes a final pass, publishing the events that handlers wrote while finishing. Pod `terminationGracePeriodSeconds` must exceed `SHUTDOWN_DELAY + SHUTDOWN_TIMEOUT`; compose uses `stop_grace_period: 30s` for the same reason.

Configuration is environment-only via `config.Loader`, which collects all missing/invalid settings and reports them at once through `Err()`.

Calls from one service to another use `httpx.NewClient(timeout)`, which bounds the call and forwards the correlation ID.

### Build and deploy

- Dockerfiles use the repo root as build context and copy only `go.mod`, `go.sum`, `pkg/` and one `services/<name>/`; the version is injected with `-ldflags -X .../pkg/platform/service.Version`. Images are distroless/nonroot.
- `deploy/local/` is the docker-compose stack. goaws emulates SNS/SQS locally; on AWS the same topic, queues, subscriptions and redrive policies are meant to come from Terraform (not yet in the repo, as is the planned `terraform/` directory referenced by `.dockerignore`).
- `docs/DECISIONS.md` records each choice with its reason and cost; `docs/TROUBLESHOOTING.md` records failures that actually happened. Add an entry to the relevant one when a decision is made or a real failure is diagnosed.

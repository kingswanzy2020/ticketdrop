# Decisions

Each entry records a choice, the reason for it, and what it costs. Entries are
added when a decision becomes real in the repository, oldest first.

## 1. The application is ticket drops, not a shop

**Decision.** The platform sells tickets in timed "drops": a sale opens at a set
moment and a limited number of tickets go to whoever orders first.

**Why.** A drop produces a real burst of load, so scaling on queue depth can be
measured instead of simulated. It also gives a result that is either true or
false: no ticket was sold twice.

**Cost.** The domain needs more care than a shop. Holding a ticket while a
payment is in progress, and releasing it if the payment fails, has to be
correct under concurrency.

## 2. One Go module, with a shared library in `pkg/platform`

**Decision.** All services live in one module. Logging, configuration, the HTTP
middleware, health probes, the database helper and the event bus are written
once in `pkg/platform` and used by every service.

**Why.** Nine services that each solve graceful shutdown or message handling
slightly differently would be nine sets of operational behaviour to learn.
With one library, a probe or a metric means the same thing everywhere.

**Cost.** A change to `pkg/platform` rebuilds every service. The CI path filter
has to treat `pkg/` and `go.mod` as belonging to all of them.

## 3. Each service has its own database

**Decision.** `orders` and `fulfillment` each own a database and never read the
other's tables. Locally the databases share one Postgres server.

**Why.** A service that owns its data can change its schema and be deployed
without coordinating with the others. What the services share is the event
contract in `pkg/contracts`, which is small and explicit.

**Cost.** No query can join across services. Checking that every order has the
right number of tickets means reading two databases and comparing, which is
what `scripts/slice-check.sh` does.

## 4. Events fan out through one SNS topic to one SQS queue per consumer

**Decision.** Every service publishes to a single topic. Each consumer has its
own queue subscribed to that topic, with a filter policy on the `event_type`
message attribute and its own dead-letter queue. Subscriptions use raw message
delivery, so the message body is the event itself.

**Why.** Several services react to the same event. With one queue per consumer,
a slow or broken consumer builds a backlog on its own queue only, and each
queue's depth is a scaling signal for exactly one service.

**Cost.** More resources to create and to watch: a queue, a dead-letter queue,
a subscription and a filter for each consumer. A wrong filter policy fails
silently, because the event is simply never delivered.

## 5. Events are published through a transactional outbox

**Decision.** A service never calls SNS while handling a request. It writes the
event to an `outbox` table in the same transaction as the change the event
describes. A relay in the same process publishes outbox rows afterwards.

**Why.** Writing to the database and then publishing are two writes that can
disagree. A crash between them leaves an order that nobody was told about.
With the outbox, the order and its event commit together or not at all.

**Cost.** Delivery is at least once: if the process dies after publishing and
before marking the row, the event is sent again. Publishing is also slightly
delayed, by up to the relay interval (250 ms by default).

## 6. Consumers record the events they have handled

**Decision.** A handler does its work inside `events.Once`. That inserts the
event ID into `processed_events` in the same transaction as the handler's own
writes, and skips the work when the ID is already there. The `tickets` table
also has a unique constraint on `(order_id, seq)` as a second guard.

**Why.** SQS delivers at least once and so does the outbox, so the same event
will arrive twice sooner or later. Recording the ID in the same transaction as
the effect makes a redelivery either repeat all of the work or none of it.

**Cost.** One extra insert for each message, and a table that grows until it is
pruned. Pruning is not built yet.

## 7. A message that cannot be handled is left for the dead-letter queue

**Decision.** When a message cannot be parsed, or its handler returns an error,
the consumer does not delete it. SQS redelivers it, and after the queue's
`maxReceiveCount` moves it to the dead-letter queue. A handler also returns an
error for an event of a type it does not expect, because that means a
subscription is wired wrongly.

**Why.** Deleting an unreadable message hides a bug. Retrying it forever blocks
a handler slot. The dead-letter queue keeps the message where a person can
inspect it, and an alert on its depth says that something is wrong.

**Cost.** A poison message is attempted `maxReceiveCount` times before it is
set aside, and each attempt occupies a handler slot for one visibility timeout.

## 8. Probes and metrics are served on a separate admin port

**Decision.** Each service serves `/healthz`, `/readyz` and `/metrics` on port
9090. The application's own routes are on 8080.

**Why.** Only 8080 is routed from outside, so metrics and probes can never be
reached through the public address. A consumer with no public API still has
probes and metrics.

**Cost.** Two ports to declare for each service in every manifest.

## 9. Liveness never checks a dependency; readiness does

**Decision.** `/healthz` reports only that the process is running. `/readyz`
fails while the service is starting, as soon as shutdown begins, and while its
database does not answer. The gateway's readiness does not check the services
behind it.

**Why.** If liveness checked the database, a database outage would make
Kubernetes restart every pod, and a restart cannot fix a database. If the
gateway's readiness depended on `orders`, one failing service would take the
whole public API out of rotation.

**Cost.** A pod that is alive but permanently unable to work is not restarted
automatically. That case has to be caught by alerts.

## 10. Shutdown follows a fixed order

**Decision.** On SIGTERM a service turns readiness off, waits `SHUTDOWN_DELAY`,
drains in-flight HTTP requests, stops its consumers and lets running handlers
finish, has the outbox relay make a final pass, and stops the admin server
last.

**Why.** Each step protects work the previous one may still produce. Requests
keep arriving for a moment after readiness turns off. Handlers that finish
during shutdown still write events, so the relay has to stop after them.

**Cost.** The pod's `terminationGracePeriodSeconds` must be longer than
`SHUTDOWN_DELAY` plus `SHUTDOWN_TIMEOUT`, or Kubernetes kills the process
partway through.

## 11. Services apply their own schema migrations at startup

**Decision.** Each service embeds its `.sql` files and applies the ones not yet
recorded in `schema_migrations` when it starts. A Postgres advisory lock makes
replicas that start together take turns.

**Why.** There is no separate migration job to order before a deployment, and a
new environment needs no manual database step.

**Cost.** During a rolling update the old version runs against the new schema,
so every migration must be compatible with the previous release. A slow
migration delays startup and has to fit within the startup probe.

## 12. The gateway lists its routes one by one

**Decision.** The gateway forwards only the method and path combinations
registered in `services/gateway/internal/proxy`. Anything else is a 404 at the
gateway.

**Why.** Forwarding a whole path prefix would publish every endpoint a service
ever adds. With an explicit list, exposing a route is a deliberate change in
one file. It also keeps the `route` metric label to a known, small set.

**Cost.** Every new public endpoint needs a change to the gateway as well as to
its service.

## 13. goaws is the local SNS and SQS emulator

**Decision.** The local stack uses `admiralpiett/goaws` in place of LocalStack.

**Why.** Since 23 March 2026 the LocalStack image needs an account and an auth
token, even on its free plan. goaws is a single 6 MB container that needs
neither. Before it was adopted it was tested for what this project relies on:
filtered fan-out, raw delivery, long polling, redrive to a dead-letter queue,
and the queue depth attributes that KEDA reads.

**Cost.** goaws supports only exact-match filter policies, on one event type or
a list of them, which is all that is used so far. It reported `ApproximateReceiveCount` as 1 on a second delivery,
so code must not depend on that attribute locally. It has no S3, so storing
tickets will need a second emulator.

## 14. In the three-service slice, fulfillment consumes `order.created` directly

**Decision.** For now `fulfillment` issues tickets as soon as an order is
created, and rendering the ticket is replaced by a configurable delay
(`SIMULATED_RENDER_TIME`).

**Why.** The slice exists to prove the path from a request to an event to a
consumer and back, before six more services are built on it. Capacity is not
enforced yet; that is the job of `inventory`.

**Cost.** This is not the final flow. Once `payments` and `inventory` exist,
`fulfillment` will be subscribed to a later event in the order's lifecycle.
Every order event carries the same data, so that is a change of filter policy
and of one constant.

**Superseded.** `inventory` and `payments` now exist. `fulfillment` consumes
`order.confirmed` and capacity is enforced (entries 15 to 18). The change was
the one predicted: a filter policy and one constant. Rendering is still the
simulated delay.

## 15. An order is accepted only after inventory has held its tickets

**Decision.** `POST /v1/orders` makes a direct HTTP call from `orders` to
`inventory` to hold the tickets, and stores the order only if the hold
succeeds. A sold-out drop is answered at once with a 409. Everything after
that point is asynchronous.

**Why.** A buyer needs to know immediately whether they got tickets. An order
is also never accepted, and a customer never charged, for tickets that are
gone.

**Cost.** Ordering now depends on `inventory` being up: when it is down or
slower than `INVENTORY_TIMEOUT`, orders are refused with a 503. The hold and
the order live in different databases and cannot share a transaction, so a
failure between the two leaves a hold with no order. Such a hold is released
when it expires (entry 21), so those tickets are out of sale for the length of
a hold.

## 16. Stock is one counter per tier, taken with one conditional UPDATE

**Decision.** `inventory` keeps the number of available tickets in a single
row per tier and takes tickets with
`UPDATE stock SET available = available - n WHERE available >= n`. A CHECK
constraint keeps the number from going below zero. Postgres is the only store.

**Why.** The comparison and the subtraction happen under one row lock, so two
orders racing for the last ticket cannot both win. There is no step where the
number is read and then written back. In a test, 400 concurrent requests for
100 tickets were granted exactly 100 holds.

**Cost.** Every hold for a tier waits on the same row lock, which puts a
ceiling on orders per second for one tier. The original plan put Valkey in
front of Postgres for this. It has been left out until a load test shows the
row is the bottleneck, because a second store brings its own consistency
problems.

## 17. A declined payment and a provider outage are handled differently

**Decision.** A decline is an answer: `payments` publishes `payment.failed`,
the order becomes `failed`, and `inventory` returns its tickets to stock. An
outage is a fault: the handler returns an error, SQS redelivers the message,
and after three attempts it goes to the dead-letter queue.

**Why.** Retrying a decline can never succeed. An outage is usually temporary,
so the same message may succeed a few seconds later.

**Cost.** A payment that reaches the dead-letter queue is not retried on its
own. Its order stays `pending` and its tickets stay held until the hold
expires (entry 21); then the order fails and the tickets return to stock. An
outage that lasts longer than a hold therefore loses those sales, though not
the tickets. If the message is moved back to the queue before the hold
expires, the order completes; after it, the payment is refused without a
charge. Both were tested.

## 18. An order has two final states and reaches them only from `pending`

**Decision.** An order ends as `ticketed` or `failed`. Both updates carry the
condition `WHERE status = 'pending'`.

**Why.** The events that end an order come from different services through a
queue with no ordering guarantee, and any of them can arrive twice. The
condition makes the first final state permanent.

**Cost.** The `orders` service does not record the steps in between. Whether a
pending order has been paid for is visible only in the other services' data
and in the logs under the order's correlation ID.

## 19. Catalog owns a drop's details, and inventory learns them when it opens

**Decision.** A drop's name, venue, opening time, tiers, prices and capacities
live in `catalog`. When the drop opens, `catalog` publishes `drop.opened` with
each tier's capacity, and `inventory` creates the stock from that event. There
is no other way to create stock. Once a drop is open, `catalog` refuses changes
to it.

**Why.** One place decides how many tickets exist. Until the event arrives,
`inventory` has no stock for the drop, so an order placed early is refused by
the same check that refuses an unknown drop. Nothing extra enforces the
opening time.

**Cost.** A drop becomes orderable a moment after its opening time, not at it:
in three test runs, between 0.4 and 0.9 seconds later. A capacity cannot be
corrected after the drop has opened; that would need a new event and a
decision about tickets already sold.

## 20. The scheduler only keeps time, and one replica at a time does it

**Decision.** `scheduler` does none of the work itself. On a timer it calls an
internal endpoint on `catalog` (open the drops that are due) and on
`inventory` (release the holds that have expired). Two replicas run; the one
holding a PostgreSQL advisory lock is the leader and runs the timers.

**Why.** The services own their data, so they must be the ones to change it.
Both endpoints are safe to call twice at once, which means correctness never
depends on there being exactly one leader: the lock avoids wasted calls, it
does not prevent damage. The lock belongs to a database session and is
released by the server when that session ends, so a killed leader needs no
cleanup. In a test the standby took over 5 seconds after the leader was
killed.

**Cost.** The scheduler needs a database connection, and a database of its
own, only to hold a lock. Kubernetes has its own mechanism for this, the Lease
object, which would remove that dependency but would not run under docker
compose. While no replica leads, drops open late and holds expire late, by up
to `LEADER_RETRY` (5 seconds).

## 21. A hold expires, and an order is never charged after its hold has

**Decision.** A hold lasts `HOLD_TTL` (10 minutes). `inventory` releases a
hold that is still unsettled `HOLD_GRACE` (30 seconds) after that, returns its
tickets to stock, and publishes `hold.expired`, which fails the order.
`order.created` carries the hold's expiry, and `payments` refuses to charge an
order once it has passed.

**Why.** Without an expiry, an order whose payment never gets an outcome keeps
its tickets out of sale for ever. Without the check in `payments`, a payment
retried long after an outage would charge a customer whose tickets had already
been sold to someone else. The grace period gives a charge that began just
before the expiry time to finish and be confirmed.

**Cost.** One gap remains. If a charge succeeds before the expiry but its
`payment.succeeded` event reaches `inventory` more than `HOLD_GRACE` late, the
hold is already gone. That message fails and goes to the dead-letter queue for
a person to refund; nothing refunds automatically. It needs `inventory` to be
down or badly backlogged for over 30 seconds at exactly the wrong moment.

## 22. Orders announces how each order ended

**Decision.** When an order reaches its final status, `orders` publishes
`order.ticketed` or `order.failed` in the same transaction as the status
change. The event carries the order, including the customer's email address,
and for a failure the reason.

**Why.** Each service that can end an order knows only its own part:
`fulfillment` has no email address, and `inventory` does not know why a
payment failed. One event from the service that owns the order says how it
turned out, whichever path led there. Because it is written with the status
change, there is exactly one for each order.

**Cost.** One more event for every order, and one more hop before the customer
hears: up to an outbox interval (250 ms). The event puts a customer's email
address on the topic and in a queue, so on AWS both hold personal data and
need encryption and tight access.

## 23. Notifications keeps no record, so a message can be sent twice

**Decision.** `notifications` has no database and does not use `events.Once`.
If the same event is delivered to its queue twice, it sends the message twice.

**Why.** A repeated email is a nuisance; a repeated charge or ticket would be
a loss, which is why every other consumer keeps a record. A record could not
prevent the repeat here anyway, because sending an email cannot be part of a
database transaction. With nothing to store, the service needs nothing
provisioned and can scale up from zero.

**Cost.** It is the one exception to the rule that every consumer is
idempotent, and it has to be remembered when a real email provider replaces
the log line. The event ID is passed as the message ID, so that a provider
which accepts an idempotency key can drop the repeat. The drop check finds
exactly one message for each order, but in runs where this service is not
interrupted.

## 24. The shopfront is rendered on the server and uses only the public API

**Decision.** `web` renders HTML with Go templates. htmx, a single script
served from the binary, swaps in the few parts that change without a reload:
the tickets left, the moment a drop opens, and an order's progress. Everything
it shows comes from the gateway's public API.

**Why.** There is no Node toolchain and nothing to build. Placing an order
works as a plain form post with JavaScript off. The shopfront has no private
way into the services, so it can do nothing the API does not allow. No page
depends on an outside CDN during a drop.

**Cost.** Every page costs an extra hop: browser to `web` to `gateway` to a
service. Progress is found by asking: a customer waiting on an order asks once
a second, and every open drop page asks for the ticket count every two
seconds. That load grows with the audience and has not been measured yet. With
JavaScript off, an order's page has to be reloaded by hand to see it finish.
Prices are shown in dollars because the catalog stores no currency.

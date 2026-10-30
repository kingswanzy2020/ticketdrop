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
what `scripts/slice-check.sh` did. That comparison now lives in
`scripts/drop-check.sh` ("orders with the wrong number of tickets"), which
reads the `orders` and `fulfillment` databases for every ticketed order.

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
in three test runs, between 0.4 and 0.9 seconds later, and in three later runs
on a different machine between 1.1 and 1.2 seconds (PROOF.md). The delay is
made of the scheduler's one-second tick, the outbox interval and the delivery
to inventory. A capacity cannot be
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
killed, and in a later run 1 second: a standby tries the lock every
`LEADER_RETRY`, so the takeover depends on where it was in that interval.

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

## 25. Kubernetes 1.36, locally and on EKS

**Decision.** The local cluster and the EKS cluster both run Kubernetes 1.36,
on the latest patch available when each is created. Locally that is Docker
Desktop's kind provisioner, which is the only one that lets the version be
chosen, with one node. kubectl stays at its newer version, which the skew
policy allows against a 1.36 server.

**Why.** KEDA is on the path that scales `fulfillment` from zero, and its
v2.21 is tested against Kubernetes 1.34 to 1.36; no KEDA release lists 1.37.
Karpenter supports 1.36 from 1.13. 1.36 is supported upstream until
2027-06-28 and on EKS until 2027-08-02, which covers the project. 1.37 was
released upstream on 2026-08-26 and reached EKS on 2026-10-01, so problems in
Traefik, CloudNativePG or KEDA charts would be met here first. Using one
version in both places means an API difference cannot make a manifest pass
locally and fail on EKS.

**Cost.** 1.37 is skipped, and an upgrade is due as 1.36 nears the end of its
support. The decision is to be revisited when KEDA's compatibility table
lists 1.37. CloudNativePG's supported versions were not checked when this was
written and need checking before its chart is pinned. The name kind mode gives
the kubeconfig context on Linux was not confirmed, and the workbook expects
`docker-desktop`.

## 26. Local images are tagged `dev`, not `latest`

**Decision.** Each service in `deploy/local/compose.yaml` names its image
`ticketdrop/<service>:${TAG:-dev}`. The tag is `dev` unless `TAG` is set, for
example to a short commit ID. Nothing in the cluster refers to `latest`.

**Why.** Kubernetes pulls an image tagged `latest` from a registry every time,
and there is no registry locally, so the pod fails with `ErrImagePull`. A
pull policy of `Always` fails the same way for any tag. A running pod does not
pick up a rebuilt image either way; it needs to be recreated. A tag that
changes with each build changes the pod spec, so a rollout follows by itself.

**Cost.** The first build creates new images beside the old
`ticketdrop-<service>:latest` ones, which are removed by hand. The pull policy
the chart uses locally is not decided here; it is set when the chart is
written. Whether the cluster can see images built on the host has not been
checked.

## 27. The emulator runs in `data` as one pod, and names itself by its cluster address

**Decision.** goaws runs in the `data` namespace as a ConfigMap, a Deployment
with one replica and a Service on port 4100. Its `Host` setting is the
Service's full in-cluster name, `goaws.data.svc.cluster.local`. Every manifest
sets `namespace:` in its own metadata and is not relied on to be applied with
`-n`. The `ticketdrop` namespace enforces the `restricted` Pod Security
standard; `data` has no Pod Security label.

**Why.** `data` holds what the services depend on, the emulator and the
Postgres cluster, and `ticketdrop` holds only the nine services; on AWS the
emulator is replaced by SNS and SQS and has no place in the cluster, and the
network rules in step 7 can let `ticketdrop` reach `data` with one rule.
goaws writes `Host` into the queue URLs it returns, so a bare `goaws`
works only for pods inside `data`; the services in `ticketdrop` need the full
name. Its queues live in memory, so one replica is the only way to have a
single set of queues. A ConfigMap mounted with `subPath` is never refreshed in
a running pod, and goaws reads the file only at start, so a change to it is
followed by a restart of the Deployment. `data` is left unenforced because the
goaws image runs as root and what the database operator's pods need has not
been checked yet (step 5).

**Cost.** Any restart of the pod, from a rollout, a drain or a crash, empties
every queue and the topic, and messages in flight are lost; the liveness probe
is lenient for that reason. The URLs it returns carry the region in front of
the host, `us-east-1.goaws.data.svc.cluster.local`, and the services ignore
them and use their own configured URLs. Nothing in `data` is checked for Pod
Security until a label is added there. The Deployment sets requests of 50m CPU
and 64Mi of memory, which were chosen, not measured.

## 28. Each service can reach only its own database, enforced by `pg_hba`

**Decision.** The Postgres cluster `pg` holds the six databases, and each has a
login role of the same name. Two `pg_hba` lines in the Cluster manifest decide
who may connect where:

```
hostssl sameuser all all scram-sha-256
host    all      all all reject
```

The first lets a role connect, over TLS and with its password, only to the
database that has its name. The second refuses every other network
connection. CloudNativePG places these after its own fixed rules and before
its default `host all all all scram-sha-256`, and Postgres uses the first rule
that matches, so `reject` wins over the default.

**Why.** Postgres grants `CONNECT` on every new database to `PUBLIC`, and
CloudNativePG's `Database` resource has no field to take it away, so without a
rule `orders` could log into `catalog`. The rule lives in the Cluster
manifest, in git, so a rebuilt cluster has it from the start and nothing has
to be remembered. A change to `pg_hba` is applied with a reload, not a
restart. The operator connects over the local socket and replication uses
certificate rules that come earlier, so neither is affected.

The alternative was `REVOKE CONNECT ON DATABASE <db> FROM PUBLIC` in each of
the six databases. That is Postgres's own privilege model and works whatever
the roles are called, and the operator leaves such grants alone. It was not
chosen because the statements are run by hand through `kubectl exec`, are not
in git, and are lost when the cluster is recreated; forgetting them on a
rebuild silently reopens every database to every role. It also leaves the
refusal to the privilege check after authentication, where `pg_hba` refuses
before a password is checked. The two can be combined later: REVOKE as a
second layer, `pg_hba` as the one that is declared.

**Cost.** `sameuser` works only while every role has the same name as its
database; a role named differently, or one that needs two databases, needs
its own line. Every network connection must use TLS, so `sslmode=disable`
anywhere, including a `DATABASE_URL`, is refused. A refusal reads
`pg_hba.conf rejects connection`, which says nothing about privileges, so the
cause has to be known to be found. Inside the database nothing is restricted:
a role that does connect is the owner of its database and can do anything in
it.

## 29. Deleting a role or database object leaves the real one in Postgres

**Decision.** The `DatabaseRole` and `Database` objects for the five services
other than catalog keep CloudNativePG's default reclaim policy, `retain`
(`databaseRoleReclaimPolicy` and `databaseReclaimPolicy`). Deleting one of
these objects from Kubernetes stops the operator managing it, and leaves the
role or the database, with its data, in Postgres.

**Why.** These databases hold orders, payments and stock. With `delete`, a
manifest removed from git, a renamed file or a `kubectl delete -f` of the
wrong directory would drop a database and everything in it. With `retain`, the
same mistake only stops the operator managing it, and applying the object
again picks it back up. Dropping something is then a deliberate act: setting
`ensure: absent` on the object, or a `DROP` run by hand.

**Cost.** Removing a service from the manifests does not remove its role or
database; they stay behind until they are dropped by hand, and nothing reports
them. Deleting the Cluster itself is not covered: its volume, and with it
every database, goes with it, whatever this policy says.

## 30. CloudNativePG is installed from its Helm chart, pinned

**Decision.** The operator is installed with the `cloudnative-pg` Helm chart,
version 0.29.1 (operator 1.30.1), as release `cnpg` in the `cnpg-system`
namespace. Its values are in `platform/cnpg/values.yaml` in the GitOps
repository and set only the operator's resources: requests of 100m CPU and
128Mi of memory, a 256Mi memory limit and no CPU limit. `cnpg-system` is
declared in `platform/namespaces.yaml` and enforces the `restricted` Pod
Security standard, which the operator's pod meets.

**Why.** The chart is what Argo CD will install on EKS, so the same version
and values file carry over. Pinning the version means a reinstall gives the
same operator, and 1.30 supports Kubernetes 1.34 to 1.36, which closes the
question left open in entry 25. The operator can create pods, Secrets and
volumes in every namespace, so nothing in its own namespace is allowed to run
as root or privileged.

**Cost.** One more tool to trust. The chart ships its CRDs as templates, so
`helm upgrade` changes them for the whole cluster, and `helm uninstall` leaves
them behind on purpose, because deleting them deletes every Cluster's data.
Under Argo CD the CRDs are too large for a client-side apply and will need
server-side apply. Moving to a newer operator means moving the chart. The
operator's resources were chosen, not measured.

## 31. One Postgres cluster, with one instance, holds all six databases

**Decision.** A single CloudNativePG `Cluster` named `pg` in `data` runs one
instance and holds the six databases. Its image is pinned to
`ghcr.io/cloudnative-pg/postgresql:18.6-system-trixie`, the same Postgres 18.6
that compose uses. It requests 250m CPU and 512Mi of memory, with a 1Gi
memory limit and no CPU limit, and sets `shared_buffers` to 256MB, a quarter of
that limit. Its volume is 2Gi on the `standard` StorageClass. Superuser access
is left off.

**Why.** One cluster is one Postgres pod and one volume on a laptop with one
node, and matches the local compose stack. On one node, more instances would
add replicas without adding failover, because they all go down with the node.
Pinning the image keeps an operator upgrade from changing the Postgres
version. No service needs a superuser.

**Cost.** A restart or a heavy query affects all six services at once, and
they are separated only by role and database, not by process or disk. With
one instance there is no failover, and `pg-ro` has no endpoints. The
`standard` class cannot grow a volume, so 2Gi is final, and it does not
enforce the size either; the write-ahead log shares the volume. The sizes were
chosen, not measured, and the pod is Burstable, where CloudNativePG recommends
Guaranteed for production. Running six clusters is to be reconsidered for
EKS.

## 32. Database passwords are made once into ignored files, and the Secrets are created from them

**Decision.** Each service's password is generated once with
`openssl rand -hex 24` into `secrets/<service>.password` in the GitOps
repository, which `.gitignore` keeps out of git. Two Secrets are created from
that file with `kubectl`: `pg-<service>` in `data`, of type
`kubernetes.io/basic-auth`, which CloudNativePG reads for the role's password,
and `<service>-db` in `ticketdrop`, holding the `DATABASE_URL` the service
reads. The commands are written out in `docs/guide/phase-2-commands.md`, and
`scripts/db-secrets.sh` runs the same steps for a rebuild. External Secrets
Operator, Sealed Secrets and SOPS were considered and not adopted now.

**Why.** Each password has one source, so the two Secrets for a service always
agree. The commands use `--dry-run=client -o yaml | kubectl apply`, so running
them again changes nothing that is right, and keeping the files lets a rebuilt
cluster get the same passwords. Typing the commands is part of learning what
each Secret is for; the script is a backup. The plan moves secrets to AWS
Secrets Manager in Phases 5 and 6, so an encrypted-in-git tool now would be
replaced soon after.

**Cost.** The password files are the only copy; losing them means new
passwords and new Secrets. The Secrets are created by hand, not by the
manifests in git, so a rebuild has a step that is not declarative. A
`DATABASE_URL` reaches a pod as an environment variable, so a changed password
needs a restart. It is not yet checked whether the Secrets Store CSI driver in
the plan can supply CloudNativePG's Secrets: the driver creates a Kubernetes
Secret only while a pod mounts it, and the operator mounts none. This is to be
settled before Phase 5.

## 33. The catalog role and database are left to the cluster's bootstrap

**Decision.** `catalog` and its owner role are created by the Cluster's
`bootstrap.initdb`, with the password from `pg-catalog`. They have no
`DatabaseRole` or `Database` object; the other five services each have both.

**Why.** The operator already keeps the owner's password in line with
`pg-catalog` every time it reconciles the Cluster. A `DatabaseRole` for an
existing role takes it over and resets every attribute it does not list, and
two controllers would then manage one password. A `Database` object would add
nothing to a database that already exists. Making `catalog` the bootstrap
database also avoids the default `app` database and role, which nothing uses.

**Cost.** `catalog` is managed differently from the other five, which has to
be remembered when looking for it: `kubectl get databaserole,database` lists
five of each, not six. The bootstrap runs only once, so the `pg-catalog`
Secret must exist before the Cluster is first applied.

## 34. Pods in `ticketdrop` must run as non-root, enforced by Pod Security Admission

**Decision.** The `ticketdrop` namespace, where the nine services run, carries
the label `pod-security.kubernetes.io/enforce: restricted`. Kubernetes refuses
any pod there that does not set `runAsNonRoot: true` and a `seccompProfile` of
`RuntimeDefault` (pod level), and `allowPrivilegeEscalation: false` and
`capabilities.drop: ["ALL"]` (container level). Only `enforce` is set, with no
pinned version, so it follows the cluster's current rules.

**Why.** The service images are already built to run as a non-root user,
distroless `nonroot` with UID 65532, so the rule costs them nothing; it turns
a property of the images into one the cluster checks. A pod that asks for
root, or a chart that leaves out the security settings, is refused when it is
created, not found later. The refusal was seen: a plain `nginx` pod, which runs
as root, was rejected (`docs/evidence/phase-2/02-restricted-rejects-root-pod.txt`).
The image's `USER` alone is not enough: Pod Security reads only the pod spec,
so the gateway image, which does run as 65532, was also refused until the
four settings were written into the pod.

**Cost.** Every pod in the namespace, including throwaway test pods, has to
carry the four settings. `runAsNonRoot` can only be checked for an image whose
user is a number; one with a user name needs `runAsUser` as well. A
read-only root filesystem is not part of `restricted`, so it is not enforced
here and has to be set by the chart if it is wanted. With `enforce` only,
there is no warning before a pod is refused. The rule covers only
`ticketdrop`: `data` is not enforced (entry 27), and `cnpg-system` is (entry
30).

## 35. The services are installed from one shared Helm chart, one release per service

**Decision.** `charts/service` in the GitOps repository is the only chart for
the nine services. Each service is its own release, named after the service
(`helm install orders charts/service -n ticketdrop -f
charts/service/values/values-orders.yaml`). `values.yaml` holds defaults
safe for every service; `values/values-<service>.yaml` holds only what
differs. Names, labels and the image (`ticketdrop/<release>:dev` unless set)
come from helpers in `templates/_helpers.tpl`. The selector uses only
`app.kubernetes.io/name`. The namespace comes from `helm -n`, not the chart.
A service's database Secret is named in `databaseSecret` and loaded with
`envFrom`; when it is empty, as for gateway, web and notifications, the pod
refers to no Secret. Every container sets a read-only root filesystem besides
the settings Pod Security requires (entry 34).

**Why.** A release per service can be upgraded, rolled back and inspected
(`helm history orders`) without touching the other eight, and maps onto one
ArgoCD Application per service in Phase 3. A first version put all nine
services in one release and looped over them in the templates; it was replaced
before it was installed; the chart now lints clean
(`docs/evidence/phase-2/06-helm-lint-clean.txt`). A Deployment's selector
cannot change once it exists,
so a label that may change, such as a version, kept out of it does not break a
later `helm upgrade`.

**Cost.** Nine releases to install and track instead of one. A template
change reaches every service at its next upgrade, so a mistake in the chart is
a mistake in nine places. Whether the chart fits services that serve only on
the admin port (payments, fulfillment, notifications, scheduler) is tested in
step 7; the `http` port is optional (entry 38). How environments are separated,
Helm values files per environment or Kustomize overlays on the rendered
output, is not decided here: there is only the local environment so far.

## 36. Pods stop with a 5s delay inside a 40s grace period, and get 80s to start

**Decision.** The chart sets `SHUTDOWN_DELAY=5s` and
`terminationGracePeriodSeconds: 40`, and leaves `SHUTDOWN_TIMEOUT` at its 25s
default. All three probes use the admin port (9090): startup and liveness ask
`/healthz`, readiness asks `/readyz` every 2s. The startup probe allows
2s × 40 = 80s. Each container requests 50m CPU and 64Mi of memory, with no
limits.

**Why.** The delay keeps the pod serving while its endpoint is removed from
the Service, so requests already on their way are answered. 40s is more than
5s + 25s, so Kubernetes does not send SIGKILL while the workers stop. The
admin server opens only after a service has waited up to 60s for Postgres and
run its migrations, so the startup probe has to outlast that, and it keeps
liveness from restarting a pod that is still booting. Seen on orders: ready
within a second of starting, and when its pod was deleted it logged
`shutdown signal received` with `delay=5s` and `stopped` 5s later, with no
timeout warning (`docs/evidence/phase-2/06-orders-shutdown-in-order.txt`).
The drain and the worker stops log only when one runs late, and the exit code
is not in the capture; a clean exit is inferred from `stopped` with no SIGKILL.

**Cost.** Every pod takes at least 5s to stop, which slows rollouts. A
service that hangs in a way `/healthz` cannot see is not restarted. The
resource requests are placeholders, not measured; without limits a pod can
use more than it asked for. With no queue backlog the drain was instant, so
how long a busy pod takes to stop has not been seen.

## 37. The chart pulls images `IfNotPresent` locally

**Decision.** `image.pullPolicy` defaults to `IfNotPresent`, with the `dev`
tag from entry 26.

**Why.** There is no registry locally, so the image has to be the one already
on the node. Docker Desktop's cluster uses the host's image store: the orders
pod ran `ticketdrop/orders:dev` as built by `make up`, which settles the
question left open in entry 26.

**Cost.** A rebuilt image with the same `dev` tag is not picked up by a
running Deployment, because the pod spec does not change; the pod has to be
deleted or the Deployment restarted. `Always` cannot be used locally. The
policy is to be looked at again when images come from ECR in Phase 4.

## 38. A service turns off the chart's `http` port with `http: null`

**Decision.** `ports.http` defaults to 8080 in the chart's `values.yaml`. A
service with no app server sets `ports: {http: null}` in its values file, and
the templates then leave the `http` entry out of both the container's ports
and the Service, using `{{- with .Values.ports.http }}`. The `admin` port
(9090) is always present, so the Service always exists. Payments,
fulfillment, notifications and scheduler set `http: null`; web, gateway,
catalog, orders and inventory keep 8080.

**Why.** The four services pass no app handler to `service.Serve`, so nothing
listens on 8080 in their pods, and a port named there would point at nothing.
One key does the job: the number is both the setting and the switch, where an
`enabled` flag beside it could disagree with it and `0` is not a port. The
Service stays because Prometheus scrapes `/metrics` on the admin port through
it. Checked with `helm template`: orders renders exactly as before, and with
`ports.http=null` both the Deployment and the Service carry only `admin`;
`helm lint` and a server-side dry run of the payments objects pass.

**Cost.** Leaving `http` out of a values file does not remove it: Helm merges
the file over the defaults, so the service still gets 8080. Only an explicit
`null` deletes a default, which is easy to miss. The number labels the port
only; the app listens on `HTTP_ADDR`, so changing the number in values
without setting `HTTP_ADDR` sends the Service's traffic to a port nothing
listens on.

## 39. A service with more than one replica gets a PDB of `maxUnavailable: 1`

**Decision.** The chart's `templates/pdb.yaml` renders a `policy/v1`
PodDisruptionBudget only when `replicaCount` is above 1. It sets
`maxUnavailable` from `pdb.maxUnavailable` (default 1), selects on the same
`service.selectorLabels` as the Deployment, and sets
`unhealthyPodEvictionPolicy: AlwaysAllow`.

**Why.** A drain, or later Karpenter consolidating nodes, evicts pods through
the eviction API, which honours the budget, so a two-replica service keeps
one pod serving while the other moves. A budget on one replica either
protects nothing (`maxUnavailable: 1`) or blocks every drain
(`minAvailable: 1`), so none is rendered. A whole number is used because a
percentage rounds, which on two or three replicas gives a different number
than intended. `AlwaysAllow` lets a drain evict a pod that is not ready,
since a crash-looping pod serves nothing and would otherwise stall the drain.
Checked with `helm template`: no PDB at one replica, and at two its
`matchLabels` equal the Deployment's; lint and a server-side dry run of all
nine services pass.

**Cost.** The budget guards only voluntary evictions: a node failing, or
`kubectl delete pod`, ignores it. A service left at one replica has no
protection and drops traffic while its pod moves. Because of `AlwaysAllow`,
a drain during a rollout can evict a new pod that has not passed readiness yet.

## 40. Replicas spread by hostname as a preference locally, by zone on EKS

**Decision.** The Deployment carries one `topologySpreadConstraints` entry
from `topologySpread` in values: `maxSkew: 1`, selecting on
`service.selectorLabels`, with `matchLabelKeys: [pod-template-hash]`. The
chart default is `topologyKey: kubernetes.io/hostname` with
`whenUnsatisfiable: ScheduleAnyway`. On EKS it is to be
`topology.kubernetes.io/zone` with `DoNotSchedule`, set by that environment's
values. How the environment values are layered is not decided yet.

**Why.** Two replicas on one node, or in one zone, go down together, which
defeats the PDB in entry 39. The local cluster has one node, labelled with a
hostname and no zone. Under a `DoNotSchedule` constraint a node without the
key's label is not eligible at all, so a zone key would leave every pod
Pending here, and hostname with `DoNotSchedule` would allow only one replica.
`ScheduleAnyway` makes the spread a scoring preference, so both replicas still
land. `pod-template-hash` makes a rolling update count only the new
ReplicaSet's pods. Locally this only proves the block renders and the pods
schedule.

**Cost.** Locally the spread does nothing, so an error in the zone setting
will first show on EKS. On EKS `DoNotSchedule` by zone leaves a replica
Pending whenever no zone has room, instead of crowding one zone. Karpenter
then has to bring up a node in the right zone.

## 41. Six services run two replicas; the three queue consumers run one

**Decision.** Web, gateway, catalog, orders, inventory and scheduler set
`replicaCount: 2` in their values files, which also gives each a PDB (entry
39). Payments, fulfillment and notifications keep the chart default of 1.

**Why.** The first five answer HTTP requests, so with one pod a drain or a
crash is an outage for callers; two replicas behind the PDB keep one serving.
The scheduler runs two so a standby is ready to take the advisory lock when
the leader goes; only the leader acts (`scheduler_leader` 1 on one pod, 0 on
the other, in `07-scheduler-one-leader.txt`). The three consumers serve no
requests: while their pod is away, messages wait in SQS and are processed
when it returns, so a gap is a delay, not an error. Fulfillment's replica
count is to be handed to KEDA, which scales it on queue depth (step 10).
Evidence: `docs/evidence/phase-2/07-nine-services-ready.txt` (15 pods Ready,
six PDBs each allowing one disruption) and `07-order-ticketed-shopfront.png`
(an order through the shopfront ending confirmed).

**Cost.** Six extra pods on a single node, all requesting CPU and memory. On
one node the second replica does not survive the node itself failing; that
needs the zone spread on EKS (entry 40). A consumer with one replica falls
behind during a drain or a crash, and payments' delay holds tickets that
much longer.

## 42. Gateway API CRDs: Standard v1.6.3, vendored, applied server-side

**Decision.** The Gateway API CRDs are the Standard channel of release
v1.6.3, downloaded unchanged into
`ticketdrop-gitops/platform/gateway-api/standard-install-v1.6.3.yaml`
(sha256 `356c2b8e286a964dc20995435659ad1095386329974793af10bbfe3eff977fe5`)
and applied with `kubectl apply --server-side`. They are installed on their
own, before Traefik, and not by the Traefik chart. The file holds ten CRDs
(GatewayClass, Gateway, HTTPRoute, GRPCRoute, ReferenceGrant,
BackendTLSPolicy, ListenerSet, TCPRoute, TLSRoute, UDPRoute) and the
`safe-upgrades` ValidatingAdmissionPolicy with its binding.

**Why.** The CRDs are a cluster-wide API that any controller reads, so they
should not be installed or removed with one controller; the Traefik chart no
longer ships them for that reason. Traefik v3.7's Gateway provider is tested
against Standard v1.6.3, and Standard is the stable set; only HTTPRoute is
needed. Keeping the file in the repo makes Git the record of what is in the
cluster, which is what ArgoCD will sync in Phase 3, turns an upgrade into a
diff, and lets a rebuild work without GitHub. A plain `kubectl apply` stores
the whole object in the `last-applied-configuration` annotation, which is
capped at 262144 bytes; the HTTPRoute CRD is about 429 KB, so only a
server-side apply fits. Evidence:
`docs/evidence/phase-2/08-gateway-api-crds.txt` (checksum, apply output, all
ten CRDs at `v1.6.3`/`standard`, Established).

**Cost.** A 1.17 MB generated file in the repo that must never be edited by
hand. Upgrades are manual and must move with the Traefik version. Deleting
the CRDs deletes every Gateway and HTTPRoute in the cluster, and deleting the
namespaces does not remove them. ArgoCD will need `ServerSideApply=true` for
this file. `safe-upgrades` refuses experimental CRDs over these and any
version below v1.5.0 until it is deliberately removed.

## 43. Traefik's CRD provider is off; routing uses only the Gateway API

**Decision.** `platform/traefik/values.yaml` sets
`providers.kubernetesCRD.enabled: false`, so Traefik does not read its own
IngressRoute and Middleware objects. The chart's Traefik CRDs are left out with
`--skip-crds`.

**Why.** The chart turns this provider on by default, which would leave two
routing APIs in the cluster. Routes here are HTTPRoutes (entry 42), a standard
that other controllers also read, so they should carry over to EKS unchanged;
IngressRoutes work only with Traefik. With the provider off, nothing would use
the chart's 25 Traefik CRDs, and Helm never upgrades CRDs it installed that way.

**Cost.** Traefik Middlewares, such as rate limiting or header rules, are not
available as Traefik objects. Using one means turning the provider back on and
applying its CRDs from a pinned file.

## 44. Traefik comes from its official chart, pinned, in a `restricted` namespace

**Decision.** Traefik is Helm release `traefik` from `traefik/traefik` chart
42.1.3 (Traefik v3.7.16), with values in `platform/traefik/values.yaml` in the
GitOps repo. Its namespace, `traefik`, is declared in `platform/namespaces.yaml`
and enforces the `restricted` Pod Security level.

**Why.** Traefik v3.7 is the version the Gateway API CRDs in entry 42 are tested
against, and a pinned chart installs the same thing on a rebuild. The chart's
default security contexts already meet `restricted` (non-root UID 65532, seccomp
`RuntimeDefault`, no privilege escalation, all capabilities dropped, read-only
root filesystem), so the level costs no overrides. Declaring the namespace in
the file, instead of `--create-namespace`, keeps its label in Git. Evidence:
`docs/evidence/phase-2/08-traefik-installed.txt` (pod 1/1, GatewayClass
`ACCEPTED True`, the Gateway provider started, no error lines in the log).

**Cost.** The chart is bumped by hand and has to stay in step with the CRDs. The
log carries `aliasHeadersStrategy` warnings for each entrypoint, about headers
reaching backends that derive variable names from header names; the default was
left as it is.

## 45. Traefik reads only the Gateway API, and the chart's other routing is off

**Decision.** Beyond the CRD provider (entry 43), the values turn off
`providers.kubernetesIngress` and `ingressClass`, and set `gateway.enabled:
false`. `gatewayClass` stays on, which creates GatewayClass `traefik`.
`experimentalChannel` stays `false`. `api.dashboard` is `true`, and no route
points at it.

**Why.** Ingress is an older API nothing here uses, and an IngressClass with
nothing behind it is noise. The chart's own Gateway is off because the Gateway is
written by hand in the next task, so its listener and the namespaces it admits are
in a file. The GatewayClass names the controller and carries no configuration
worth owning, so the chart can keep it. Experimental mode would make Traefik look
for kinds the Standard CRDs do not all carry.

**Cost.** Nothing answers until the Gateway and HTTPRoutes exist: `localhost:8088`
returns Traefik's own 404. The dashboard can be reached only by port-forward, and
routing it through the Gateway later would make it public.

## 46. The public port is 8088, Traefik listens on 8000, and HTTPS is not exposed

**Decision.** `ports.web.exposedPort` is 8088 and `ports.web.port` stays 8000.
`ports.websecure.expose.default` is `false`. `ports.traefik` is 8080 inside the
pod and is not exposed. The Service is a `LoadBalancer` with the default
`externalTrafficPolicy: Cluster`. The Gateway listener has to use 8000, the
entrypoint inside the pod, not the Service's 8088.

**Why.** 8080 is already taken on the host, and the host's unprivileged port range
starts at 1024, so the chart's defaults of 80 and 443 were ruled out without being
tried. There is no TLS locally. 8000 is the chart's default; `web` cannot share
8080 with the `traefik` entrypoint in one pod. `Cluster` is the default, and with
one node `Local` would have nothing to choose between.

This settles how Docker Desktop's kind-based cluster exposes a LoadBalancer, as
observed in `docs/evidence/phase-2/08-loadbalancer-first-address.txt`: the Service
received an address about a second after the install (`EnsuredLoadBalancer`),
`172.18.0.5` with `ipMode: Proxy`, and the host listens on `0.0.0.0:8088`.
`curl localhost:8088/` gets Traefik's 404. `curl 172.18.0.5:8088/` from the host
times out, so the EXTERNAL-IP is not the address to use here; `localhost` is. What
holds the host port was not identified. A LoadBalancer on this cluster can be
reached without a port-forward, so no NodePort fallback is needed.

**Cost.** The port is bound on all host interfaces (`0.0.0.0`), so with no firewall
rule the shop is open to the local network. Any other LoadBalancer Service on this
cluster needs a different free port. On EKS the port and the address both change,
and HTTPS comes with a certificate. The capture was taken about five minutes after
the install, not during it; it rests on the Service's events and status.

## 47. Traefik runs one replica, with limits as well as requests, and access logs on

**Decision.** `deployment.replicas` is 1. Traefik requests `100m` CPU and `128Mi`
of memory and is limited to `500m` and `512Mi`. `accessLog.enabled` is `true`, and
both the access log and the Traefik log are JSON at level `INFO`.

**Why.** One local node gains little from a second replica, and the chart's PDB is
off. The chart's default of no requests would break the rule that every container
has them. The limits stop one component from taking the laptop's resources. An
access log line per request shows which router and backend answered, which is how
a routing problem gets diagnosed here.

**Cost.** One replica means the only way in goes down during a Traefik restart or a
drain, so EKS needs two or more with a PDB. The CPU limit differs from the nine
services and the CloudNativePG operator, which have none (entries 30 and 36): under
load Traefik is throttled at half a core, so a load test can measure that limit and
not the services behind it. Access logs add a line per request, which adds up
during a load test.

## 48. One Gateway in `traefik` admits routes from `ticketdrop` only, through one HTTPRoute

**Decision.** Gateway `ticketdrop` lives in the `traefik` namespace with one HTTP
listener on port 8000 and no hostname. Its `allowedRoutes` uses a `Selector` on
`kubernetes.io/metadata.name: ticketdrop`. One HTTPRoute, also named `ticketdrop`,
lives in the `ticketdrop` namespace with two rules: `PathPrefix /v1` goes to Service
`gateway` on 8080, and `PathPrefix /` goes to Service `web` on 8080. Both files are
in `platform/routes/` in the GitOps repo.

**Why.** The listener port has to be the `web` entrypoint inside the Traefik pod
(entry 46), not the Service's 8088. With no hostname the listener answers
`localhost`. The Gateway is the platform's front door and the routes belong to the
app, so they sit in their own namespaces, and a `Selector` admits only the namespace
that should publish through it, where `All` would admit any. `/v1` goes to the API
gateway, not to a service directly, so only the routes it lists are reachable: in
`docs/evidence/phase-2/08-routes-through-gateway.txt`,
`/v1/drops/x/availability` gets inventory's JSON 404, and `/v1/healthz` gets a plain
404 from the gateway, which Traefik's access log confirms. The most specific match
wins whatever the order, so `/v1` is never taken by `/`. One route with two rules is
fewer objects than two, and both paths change together. `08-shopfront-through-gateway.png`
shows the shopfront through this path.

**Cost.** Every path shares one route object, so a bad edit can affect both, and its
status does not separate them. The listener has no hostname and no TLS; EKS needs
both, and the listener's port changes with the entrypoint. A new namespace that
publishes routes needs the selector changed. The Gateway and the route need
`kubectl apply` by hand until ArgoCD syncs them in Phase 3.

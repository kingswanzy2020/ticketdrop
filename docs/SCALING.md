# What I would do differently at a different scale

TicketDrop is sized to prove correctness on a laptop. This page says what I
would change if the crowd were much smaller or much larger, what would tell me
a change is needed, and what each change would cost.

One rule runs through all of it: **measure before changing.** Nothing below has
been load-tested yet. Where I name a bottleneck it is because the code makes it
a candidate, not because I have seen it fail. That is the same rule that kept
Valkey out of the design
([DECISIONS 16](DECISIONS.md#16-stock-is-one-counter-per-tier-taken-with-one-conditional-update)).

---

## Where it is today

The numbers that set its limits, from the code and `deploy/local/compose.yaml`:

| Thing | Value | Where it comes from |
|---|---|---|
| Stock rows per tier | 1 | Every hold for a tier takes the same row lock |
| Hold call, orders → inventory | 2 s timeout (`INVENTORY_TIMEOUT`) | Past it, the order is refused with a 503 |
| Outbox relay | every 250 ms when idle, 100 rows per pass | `OUTBOX_INTERVAL`, `OUTBOX_BATCH`; a full batch goes straight round again |
| Fulfillment throughput | about 20 orders/s per replica | `CONSUMER_CONCURRENCY=4` × one order per `SIMULATED_RENDER_TIME=200ms` |
| Queue visibility timeout | 10 s | `deploy/local/goaws.yaml`; `CONSUMER_TIMEOUT` is 5 s, below it |
| Shopfront polling, per open browser | 1 req/s while waiting for the drop to open; 1 every 2 s for tickets left once it is open; 1 req/s while an order is pending | `hx-trigger` in `services/web/internal/shop/templates/fragments.html` |
| Backend calls per poll | 1 before opening (catalog); 2 after (catalog and inventory) | `Shop.loadDrop` |
| Postgres | one server, one database per service | `deploy/local/compose.yaml` |
| Replicas | one of each service, two of the scheduler | Same |
| What the checks prove | 600 orders for 1,000 tickets, 16 customers at a time | `scripts/drop-check.sh` |

---

## Smaller: what I would build for one venue

*A few hundred tickets, a few hundred buyers, one small team.*

**I would not build nine services.** One Go binary, one Postgres database, and a
`jobs` table polled with `FOR UPDATE SKIP LOCKED` in place of SNS and SQS. One
deploy, one thing to watch, no network between the steps of an order.

**What I would keep,** because it is about correctness and not about scale:

- The hold as one conditional `UPDATE ... WHERE available >= n`.
- Accepting an order only after its tickets are held.
- Holds that expire, so an unanswered payment cannot keep tickets for ever.
- A decline handled differently from a provider error.
- Never charging after a hold has expired, and an idempotency key on every charge.
- Final order states reached only from `pending`.

**Why the project does not do this:** its purpose is to practise what only
exists between services, as the [case study](CASE_STUDY.md#why-nine-services-for-a-system-this-size)
says. That is a portfolio reason, not an engineering one, and I would say so.

**The team is a scale too.** Nine services suit several teams that each own a
part and deploy on their own schedule; that is what a database per service and
a small, explicit event contract buy. For one person or one team, the same
boundaries are better as packages inside one binary, and can be split out later
along the lines this project has already drawn.

---

## Ten times the crowd

*About 10,000 buyers in the first minute for about 1,000 tickets: the example in
the field guide.*

Below, each likely limit comes with the signal that would confirm it and the
change I would make.

### 1. The single stock row per tier

**Why it is a candidate.** Every hold for a tier waits on one row lock. The
statement is short, but at a few thousand attempts a second for one tier they
queue.

**Signal.** Hold latency at p99 rising with load while inventory's CPU stays low;
lock waits on `stock` in `pg_stat_activity`; orders refused with 503 because the
2 s hold timeout was reached.

**Change.** Split each tier's stock across *K* rows (buckets). A hold picks a
bucket at random and tries the same conditional `UPDATE`; if that bucket is
empty it tries the others. Overselling is still impossible, because each bucket
is still guarded by its own conditional update and `CHECK` constraint.

**Cost.** "Sold out" now means every bucket is empty, so the last few tickets
take several tries. The count shown to buyers is a sum. A request for 4 tickets
can fail while 4 remain spread across buckets, unless holds may span buckets.

### 2. The shopfront's polling

**Why it is a candidate.** 10,000 people waiting on a drop page make 10,000
requests a second, each going browser → web → gateway → catalog, for an answer
that is the same for all of them. Once the drop opens, the ticket counts make
5,000 requests a second, each calling both catalog and inventory.

**Signal.** Request rate on web and gateway several times the order rate; catalog
and inventory latency rising before the sale has really started.

**Change.**

- Cache the drop and its availability for one second at the gateway or a CDN.
  Every viewer sees the same numbers, so one request a second does the work of
  thousands. A count one second old is fine for display; the hold is what decides.
- Add jitter to the polling intervals so browsers do not ask in lockstep.
- For an order's progress, use server-sent events or a longer interval with
  backoff: the order usually ends in a second or two.

**Cost.** A cache layer to run and to invalidate. Counts may show a ticket that
has just gone, which the hold already handles with a clear "sold out".

### 3. Database connections

**Why it is a candidate.** Each replica of each service keeps its own pool. Scaling
consumers out on queue depth multiplies connections, and Postgres has a fixed
`max_connections`.

**Signal.** Connection errors or pool waits as replicas are added; Postgres
memory rising with connection count.

**Change.** A connection pooler (PgBouncer in transaction mode, or RDS Proxy on
AWS), and pool sizes set from the number of replicas, not left at the default.

**Cost.** Transaction pooling breaks session-level features. **The scheduler's
advisory lock is session-level**, so the scheduler must keep a direct
connection, or move to a Kubernetes `Lease`
([DECISIONS 20](DECISIONS.md#20-the-scheduler-only-keeps-time-and-one-replica-at-a-time-does-it)).
Migrations take an advisory lock too
([DECISIONS 11](DECISIONS.md#11-services-apply-their-own-schema-migrations-at-startup)).

### 4. Consumers that cannot keep up

**Why it is a candidate.** Fulfillment is throttled to about 20 orders a second
per replica, on purpose, so a backlog builds.

**Signal.** `ApproximateNumberOfMessages` and the age of the oldest message on
the queue.

**Change.** Scale each consumer on its own queue's depth with KEDA, from zero
replicas, and let Karpenter add nodes for the pods. That is the plan for
Phases 2 and 5, and why each consumer has its own queue
([DECISIONS 4](DECISIONS.md#4-events-fan-out-through-one-sns-topic-to-one-sqs-queue-per-consumer)).

**Cost.** A cold start on the first message after an idle period. A minimum of
one replica during a scheduled drop avoids it.

### 5. No limit at the front door

**Why it is a candidate.** The gateway forwards every listed route with no rate
limit. One script can place orders as fast as the network allows.

**Change.** A rate limit per client and per route at the edge, a cap on tickets
per customer per drop, and bot protection in front of `POST /v1/orders`.

**Cost.** Real customers behind one NAT can be throttled together; limits need
tuning against real traffic.

### Housekeeping that grows with volume

- **`processed_events` is never pruned**
  ([DECISIONS 6](DECISIONS.md#6-consumers-record-the-events-they-have-handled)).
  Delete rows older than the longest possible redelivery window (the queue's
  retention period), or partition the table by day and drop old partitions.
- **The outbox is pruned an hour after publishing**, which is already enough.
- **Expired holds are found by a sweep every five seconds.** It already reads a
  partial index on unsettled holds (`holds_expiring`), so it grows with the
  number of holds still waiting, not with every hold ever taken.

---

## A national on-sale

*Hundreds of thousands of buyers in the first seconds, many tiers, assigned seats.*

At this scale the shape changes, not just the size of each part.

**A virtual waiting room in front of everything.** Buyers join a queue before
the drop opens and are admitted at the rate the hold path can sustain, each with
a signed token that `POST /v1/orders` requires. This is the most important change:
it turns a spike the system must survive into a flow the system chooses. It also
gives buyers an honest position in line instead of errors.

**Stock in memory, Postgres as the record.** Holds against a counter in Valkey
(an atomic decrement in a script), with the hold written to Postgres
asynchronously and reconciled. This is what DECISIONS 16 deferred. *Cost:* two
stores that can disagree, and a reconciliation job whose correctness matters as
much as the hold's.

**Assigned seats change the model.** A counter per tier becomes a row per seat,
and a hold becomes "reserve these specific seats". Contention moves from one
row to the popular seats. The conditional-update idea survives as
`UPDATE seats SET held_by = $1 WHERE id = ANY($2) AND held_by IS NULL`, checked
for the number of rows changed.

**A database instance per service, not a database per service on one server.**
Separate failure domains, separate sizing, separate credentials. Catalog, which
is read-heavy and changes rarely, gets read replicas or sits behind the cache.

**The outbox relay becomes change data capture**, such as Debezium reading the
write-ahead log, if polling the outbox table becomes a measurable load.

**The payment provider becomes the limit.** Providers rate-limit. Payments would
need a concurrency cap, the provider's own idempotency key on every charge
(the event ID already fits), and automatic refunds instead of the dead-letter
queue for a payment that lands after its hold was released.

**Multiple availability zones from the start**, and a rehearsed answer to "what
happens to orders in flight when a zone goes". Several regions only if the
business needs it: stock for one drop is a single source of truth, and splitting
it across regions brings back every consistency problem this project was built
to avoid.

---

## What would not change at any scale

- The transactional outbox. Dual writes are wrong at every size.
- Idempotent consumers, recorded in the same transaction as the work.
- Holds that expire, and no charge after expiry.
- A decline treated as an answer and an outage treated as a fault.
- Exactly one place that decides how many tickets exist.
- Proving each promise by breaking the running system on purpose, and writing
  down what each choice costs.

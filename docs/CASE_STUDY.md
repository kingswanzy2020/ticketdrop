# TicketDrop: case study

This is the write-up of the project in five parts: the problem, the approach and
why I chose it, what broke, what I changed, and what I would do differently at
a different scale. Each claim links to the record it comes from:
[DECISIONS.md](DECISIONS.md) for choices, [TROUBLESHOOTING.md](TROUBLESHOOTING.md)
for failures, and [PROOF.md](PROOF.md) for the tests and their results.

---

## 1. The problem

A concert goes on sale at 10:00. At 9:59 nobody is buying. At 10:00 ten
thousand people press Buy in the same second, and there are a thousand tickets.
That moment is a **drop**.

A drop is hard on a system in two ways:

1. **Correctness under concurrency and failure.** Many buyers race for the last
   ticket while services crash, restart and redeliver messages. The system must
   still give each ticket to exactly one person.
2. **A burst of load.** The system needs far more capacity for ten minutes than
   for the rest of the day, so it has to grow quickly and shrink again.

I chose ticket drops over a generic shop because both problems give a result
that is either true or false: no ticket was sold twice, and the system scaled
in so many seconds ([DECISIONS 1](DECISIONS.md#1-the-application-is-ticket-drops-not-a-shop)).

### What the system promises

I wrote down five promises before choosing an architecture, and every decision
since has been measured against them:

| Promise | What breaking it looks like |
|---|---|
| Never oversell | Two customers hold the same ticket |
| Never lose an order | A customer was told "accepted" and nothing ever happened |
| Never charge twice, or for tickets that are gone | A redelivered message charges a card again, or a late retry charges for tickets already resold |
| Never leave an order stuck | An order sits in `pending` for ever, and its tickets with it |
| Tell each customer once how it ended | Silence, or the same email several times |

The last one is the weakest, on purpose: see [section 4](#a-rule-broken-on-purpose).

### The constraints I set myself

- **Run the whole thing on a laptop for free**, with the same shape it would have
  on AWS: SNS and SQS emulated, Postgres per service, nothing that needs an
  account or a token.
- **Prove each promise against the running system**, not just in unit tests,
  and while things are being broken.
- **Write down every choice with its cost.** If I could not name the cost, I
  had not understood the choice.

---

## 2. The approach, and why

Nine Go services, each with its own Postgres database, talking through events:
**outbox table → relay → SNS topic → one SQS queue per consumer → idempotent
handler**. The only synchronous calls are the ticket hold (orders → inventory)
and the scheduler's two timers.

![The journey of one order](diagrams/order-journey.svg)

### Why nine services for a system this size

Honestly: a system this size does not need nine services. A single Go binary
and one Postgres database, using the same conditional `UPDATE`, would sell a
thousand tickets correctly. I split it because the purpose of the project is
to practise the problems that only exist once there are several services:
partial failure, at-least-once delivery, independent deployment, per-service
scaling, and shutdown in the middle of work. Those are the problems a platform
engineer is paid to handle, and a monolith would have hidden every one of them.
[SCALING.md](SCALING.md#smaller-what-i-would-build-for-one-venue) says what I
would build instead for a real, small customer.

The split is not arbitrary. Each service owns one thing the business cares
about (stock, money, tickets, the order, the drop), and the boundaries fall
where the promises need different guarantees.

### How each promise is kept

**Never oversell.** Stock is one row per tier, and a hold is one statement:
`UPDATE stock SET available = available - n WHERE available >= n`, with a
`CHECK` constraint that stops the count going below zero. The comparison and the
subtraction happen under one row lock, so two buyers cannot both take the last
ticket ([DECISIONS 16](DECISIONS.md#16-stock-is-one-counter-per-tier-taken-with-one-conditional-update)).

- *Rejected:* reading the stock and then writing it (a race between the two
  steps); a queue of buyers in front of stock, or Valkey as the counter (another
  component to run, and two stores that must agree).
- *Cost:* every hold for a tier waits on the same row lock. That is a ceiling on
  orders per second for one tier, which I have not yet measured.

An order is accepted only after inventory has held its tickets, through a
direct HTTP call. The buyer hears "sold out" at once, and nobody is ever charged
for tickets that do not exist ([DECISIONS 15](DECISIONS.md#15-an-order-is-accepted-only-after-inventory-has-held-its-tickets)).
The cost is that ordering depends on inventory being up: when it is not, orders
are refused with a 503.

**Never lose an order.** A service cannot write to its database and publish to
SNS atomically. If it crashes between the two, either the event is lost or an
event is published for a change that never happened. So each service writes the
event to an `outbox` table **in the same transaction** as the change, and a relay
publishes it afterwards ([DECISIONS 5](DECISIONS.md#5-events-are-published-through-a-transactional-outbox)).
Each consumer has its own queue, so a consumer that is down finds its messages
waiting when it comes back ([DECISIONS 4](DECISIONS.md#4-events-fan-out-through-one-sns-topic-to-one-sqs-queue-per-consumer)).

- *Rejected:* publishing to SNS straight after the commit, which is the
  dual-write problem with a smaller window.
- *Cost:* delivery is at least once, and up to 250 ms late.

**Never charge twice.** Because delivery is at least once, every message will
sooner or later arrive twice. Each handler runs inside `events.Once`, which
records the event ID in `processed_events` in the same transaction as the work,
so a redelivery does all of the work or none of it
([DECISIONS 6](DECISIONS.md#6-consumers-record-the-events-they-have-handled)).
The `payments` and `tickets` tables also have unique constraints as a second
guard. Payments refuses to charge an order whose hold has expired, because its
tickets may already belong to someone else
([DECISIONS 21](DECISIONS.md#21-a-hold-expires-and-an-order-is-never-charged-after-its-hold-has)).

- *Cost:* one extra insert per message, and a table that grows until it is
  pruned. Pruning is not built yet.

**Never leave an order stuck.** Four mechanisms, each covering a different way
of getting stuck:

| Way to get stuck | What prevents it |
|---|---|
| The payment never gets an answer | Holds expire after `HOLD_TTL` (10 min) plus `HOLD_GRACE` (30 s); the tickets return to stock and the order fails ([21](DECISIONS.md#21-a-hold-expires-and-an-order-is-never-charged-after-its-hold-has)) |
| A message can never be handled | After three attempts SQS moves it to a dead-letter queue for a person, instead of blocking the line ([7](DECISIONS.md#7-a-message-that-cannot-be-handled-is-left-for-the-dead-letter-queue)) |
| Events arrive late, twice, or out of order | An order leaves `pending` only once: both final updates carry `WHERE status = 'pending'` ([18](DECISIONS.md#18-an-order-has-two-final-states-and-reaches-them-only-from-pending)) |
| The process that expires holds dies | The scheduler runs as two replicas; the one holding a Postgres advisory lock leads, and the lock is released by the server when the leader's session ends ([20](DECISIONS.md#20-the-scheduler-only-keeps-time-and-one-replica-at-a-time-does-it)) |

The scheduler itself does no work. It only tells catalog and inventory that time
has passed, and both endpoints are safe to call twice at once. So two leaders
for a moment waste a call; they cannot do damage. Correctness never depends on
the leader election being perfect.

**A decline is not an outage.** A declined card is the customer's answer: the
order fails and the tickets go back. A provider error is our problem: the
message is retried and then dead-lettered
([DECISIONS 17](DECISIONS.md#17-a-declined-payment-and-a-provider-outage-are-handled-differently)).
Retrying a decline can never succeed; failing an order because of our own
outage would turn away a customer who could have paid.

### The operational shape

The same library (`pkg/platform`) gives every service the same probes, metrics,
configuration and shutdown, so a probe or a metric means the same thing
everywhere ([DECISIONS 2](DECISIONS.md#2-one-go-module-with-a-shared-library-in-pkgplatform)).
Liveness checks nothing but the process; readiness checks the database, so a
database outage does not make Kubernetes restart every pod
([DECISIONS 9](DECISIONS.md#9-liveness-never-checks-a-dependency-readiness-does)).
Probes and metrics are on a separate admin port that is never routed from
outside, and the gateway forwards only routes it lists one by one
([DECISIONS 8](DECISIONS.md#8-probes-and-metrics-are-served-on-a-separate-admin-port),
[12](DECISIONS.md#12-the-gateway-lists-its-routes-one-by-one)).

---

## 3. How I tested it, and how I broke it

Unit tests cover the pieces. The promises are proved by two scripts that run
against the whole stack while it is being damaged. The full list of what they
assert, and the results of the latest runs, is in [PROOF.md](PROOF.md).

**`make drop-check`** announces a drop and waits for it to open on its own,
places 600 orders for 1,000 tickets (each for 1 to 4 tickets, so demand exceeds
supply), and **SIGKILLs** payments, inventory or fulfillment mid-run. SIGKILL, not
SIGTERM: no graceful shutdown, as when a node disappears. Then it checks:

- tickets issued ≤ capacity, and each ticketed order holds exactly its quantity
- every accepted order ended `ticketed` or `failed`; no hold is left waiting
- tickets gone from stock = tickets issued; holds released = orders failed
- each customer was told once
- replaying events that were already delivered changes nothing, at every hop
- a poison message reaches the dead-letter queue

**`make outage-check`** takes the payment provider down, shortens holds to 40
seconds, kills the scheduler's leader, and checks that the standby took over,
the unpayable orders failed when their holds expired, the tickets went back to
stock, and that retrying the dead-lettered payments afterwards charges nobody.

The latest runs, one drop-check for each service that can be killed and one
outage-check, all passed: no ticket oversold, no order lost or stuck, nobody
charged after a hold expired ([results](PROOF.md#results)).

Testing the system as a whole, rather than service by service, is what found
the one real bug so far.

---

## 4. What broke, and what I changed

### The orders that stayed `pending` after a clean shutdown

The most instructive failure ([TROUBLESHOOTING 3](TROUBLESHOOTING.md#3-after-a-graceful-stop-some-orders-stayed-pending-although-their-tickets-existed)).

**What I saw.** I stopped fulfillment with SIGTERM while it was working through
60 orders. It exited cleanly with code 0. Afterwards 20 orders had tickets, but
only 12 were marked `ticketed`. Eight `ticket.issued` events sat unpublished in
its outbox until the service next started.

**Why.** Every background worker was told to stop at the same moment. The
consumer correctly let its running handlers finish, and those handlers wrote
their events to the outbox, after the relay had already stopped.

**What I changed.** Workers now stop one at a time, last registered first. Each
service registers its relay before its consumer, so the relay stops last, and
it makes a final, time-bounded pass before it returns. The same test now leaves
the outbox empty.

**What it taught me.**

- *A clean exit is not a correct exit.* Exit code 0, no errors in the log, and
  still a visible inconsistency for customers.
- *The order of shutdown is a design decision*, and it became one
  ([DECISIONS 10](DECISIONS.md#10-shutdown-follows-a-fixed-order)). It has a
  direct consequence for Kubernetes: `terminationGracePeriodSeconds` must be
  longer than `SHUTDOWN_DELAY + SHUTDOWN_TIMEOUT`, or the kubelet kills the
  process half way through the sequence.
- *The outbox was already doing its job.* Nothing was lost; it was late. With a
  single replica, late meant "until the next deploy". That is the difference
  between a design that fails safe and one that fails silently.

### Decisions whose cost produced the next decision

Several changes were not bugs but the cost column of one decision turning into
the next one. This chain is the clearest example:

1. **Decline vs outage** ([17](DECISIONS.md#17-a-declined-payment-and-a-provider-outage-are-handled-differently)).
   An outage is retried and then dead-lettered. *Cost:* the order stays
   `pending` and its tickets stay held, for ever.
2. **So holds expire** ([21](DECISIONS.md#21-a-hold-expires-and-an-order-is-never-charged-after-its-hold-has)).
   An unsettled hold is released after 10 minutes plus 30 seconds' grace and the
   order fails. *New risk:* someone moves the dead-lettered payment back to the
   queue an hour later, and the customer is charged for tickets that were
   resold.
3. **So payments checks the hold's expiry**, which `order.created` now carries,
   and refuses to charge after it. `make outage-check` proves exactly this
   sequence.
4. **One gap is left, and written down:** a charge that succeeds just before
   expiry but reaches inventory more than 30 seconds late. It goes to the
   dead-letter queue for a person to refund. I chose to document it rather than
   build automatic refunds now, because it needs inventory to be down at
   exactly the wrong moment.

Others in the same pattern:

- **The first slice was deliberately incomplete.** Fulfillment first consumed
  `order.created` directly, to prove the event path end to end before six more
  services depended on it. When inventory and payments arrived, moving it to
  `order.confirmed` was one filter policy and one constant, as the decision had
  predicted ([14](DECISIONS.md#14-in-the-three-service-slice-fulfillment-consumes-ordercreated-directly)).
- **Nobody knew enough to tell the customer.** Fulfillment has no email address
  and inventory does not know why a payment failed. So the service that owns the
  order announces how it ended, in the same transaction as the status change
  ([22](DECISIONS.md#22-orders-announces-how-each-order-ended)). *Cost:* a
  customer's email address now travels on the topic, so on AWS the topic and
  queues hold personal data.

### A rule broken on purpose

Every consumer is idempotent except notifications, which keeps no record and
will send a message twice if the event arrives twice
([23](DECISIONS.md#23-notifications-keeps-no-record-so-a-message-can-be-sent-twice)).
A repeated email is a nuisance; a repeated charge is a loss. And a record could
not prevent the repeat anyway: sending an email cannot be inside a database
transaction. Knowing when a rule does not buy anything is part of applying it.

### The environment broke too

- **LocalStack started requiring an account and a token** in March 2026. I
  replaced it with goaws, but only after testing it for everything the project
  depends on: filtered fan-out, raw delivery, long polling, redrive to a
  dead-letter queue, and the queue-depth attributes KEDA reads. The test found
  that goaws reports `ApproximateReceiveCount` wrongly, so no code relies on it
  ([13](DECISIONS.md#13-goaws-is-the-local-sns-and-sqs-emulator)).
- **goaws ignored its config file** because the image reads a different path
  from the one older examples use. Listing the image's filesystem found it
  ([TROUBLESHOOTING 2](TROUBLESHOOTING.md#2-goaws-ignores-its-configuration-file)).
- **Docker Desktop refused a bind mount from `/tmp`**, which it does not share
  with its VM ([TROUBLESHOOTING 1](TROUBLESHOOTING.md#1-docker-desktop-refuses-a-bind-mount-from-tmp)).

The lesson from all three: check what the tool actually does before building on
what its documentation or an example says it does.

---

## 5. What is not handled, and what I would revisit

### Known gaps

| Gap | Why it is acceptable for now | What would fix it |
|---|---|---|
| A payment that succeeds after its hold was released goes to the DLQ for a manual refund | Needs inventory down or backlogged for over 30 s at exactly the wrong moment | A `refund.requested` event consumed by payments |
| Notifications can send a message twice | A duplicate email is a nuisance, not a loss | An idempotency key at the email provider (the event ID is already passed as the message ID) |
| `processed_events` is never pruned | Small at this volume | Delete rows older than the longest possible redelivery window, or partition by day |
| Tickets from declined payments return to stock after other buyers were already told "sold out"; in the latest runs 49 to 86 of 1,000 went unsold ([PROOF](PROOF.md#observations)) | Correct, only lost sales; found by reading a passing run, not a failing one | A waitlist, or answering "none left right now, try again shortly" |
| No load test with a real crowd; the checks send 16 customers at a time | Correctness first; the row lock may never be the bottleneck | A k6 test against a drop, recording hold latency and lock waits |
| Shopfront polling load is unmeasured (1 request/s per waiting customer) | Same as above | Measure first; see [SCALING.md](SCALING.md) |
| Locally, all databases share one Postgres server and one login | It is a laptop | Separate roles per service on the operator in Phase 2; separate credentials on AWS |

### What I would revisit

These come from the cost column of decisions that were right at the time:

- **The scheduler holds a database connection only to hold a lock**
  ([20](DECISIONS.md#20-the-scheduler-only-keeps-time-and-one-replica-at-a-time-does-it)).
  On Kubernetes a `Lease` object would do the same job without a database. I
  kept the advisory lock because it also works under docker compose.
- **An order does not record the steps in between**
  ([18](DECISIONS.md#18-an-order-has-two-final-states-and-reaches-them-only-from-pending)).
  Answering "why is this order still pending?" means reading three other
  databases and the logs under the correlation ID. A support team would want an
  order timeline built from the events.
- **The hold and the order live in different databases**
  ([15](DECISIONS.md#15-an-order-is-accepted-only-after-inventory-has-held-its-tickets)).
  A crash between the two leaves a hold with no order, which keeps those tickets
  off sale for the length of a hold. It is safe, but on a drop that sells out in
  seconds those tickets are lost sales. A shorter hold for orders that never
  reach `orders` would narrow it.

---

## 6. What I would do differently at a different scale

The full answer, with the signal that would tell me each change is needed, is in
[SCALING.md](SCALING.md). In short:

- **Smaller** (one venue, a few hundred tickets): one service, one database, a
  jobs table instead of SNS and SQS. I would keep the conditional `UPDATE`, the
  expiring hold and the idempotent payment, because those are about correctness,
  not scale.
- **Ten times the crowd:** the first limits are likely the single stock row per
  tier, the shopfront's polling, and Postgres connections. I would split each
  tier's stock across several rows, cache availability for a second at the
  edge, put a connection pooler in front of Postgres, and scale consumers on
  queue depth with KEDA.
- **A national on-sale:** a virtual waiting room in front of everything, admitting
  buyers at the rate the hold path can sustain; stock in memory with Postgres as
  the record; a database instance per service; bot protection at the edge.

What would not change at any scale: the outbox, idempotent consumers, holds that
expire, a decline treated differently from an outage, and proving each promise
by breaking the system on purpose.

---

## 7. Where it goes next

The services and the local stack are built. The next phases move them onto real
infrastructure, which is the part of the project that is mine to write:

1. Kubernetes on a laptop: one Helm chart for all nine services, a database
   operator, a Gateway API front door, network policies, and KEDA scaling
   fulfillment from zero on queue depth.
2. CI that lints, tests, builds and scans only what changed.
3. AWS with Terraform: network, EKS, SNS and SQS, secrets and permissions,
   split so the expensive parts can be destroyed after each session.
4. GitOps with Argo CD for every cluster add-on and every release.
5. Dashboards and alerts in Git, each alert set off once on purpose.
6. Drills on EKS: a node killed, a Spot interruption, a bad release rolled back,
   a database failover, with a number and a screenshot for each.
7. Destroy everything, rebuild it from Git, and time it.

Each phase adds entries to [DECISIONS.md](DECISIONS.md) and, when something
really breaks, to [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

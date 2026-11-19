# Proof

What the system promises, what could break each promise, how it is broken on
purpose, and what happened when it was. Unit tests cover the pieces; this page
is about the system as a whole, running.

Three scripts do the work, against the local stack started by `make up`:

- `make drop-check` (`scripts/drop-check.sh`), about two minutes. Run it once for
  each value of `KILL` after changing anything on the event path.
- `make outage-check` (`scripts/outage-check.sh`), about two minutes. Run it after
  changing holds, payments or the scheduler.
- `make demo-drop` (`scripts/demo-drop.sh` and `loadtest/drop.js`), under a
  minute. A k6 crowd waits on the shopfront for a drop to open, then buys; the
  script then checks the databases. It measures; it injects no faults.

---

## Promises, threats and checks

| Promise | What could break it | Mechanism that keeps it | Assertion that proves it |
|---|---|---|---|
| Never oversell | Many buyers racing for the last ticket | One conditional `UPDATE` per hold, `CHECK (available >= 0)` ([16](DECISIONS.md#16-stock-is-one-counter-per-tier-taken-with-one-conditional-update)) | *tickets issued within capacity*; *orders with the wrong number of tickets* = 0; *tickets gone from stock equals tickets issued* |
| Never lose an order | A service killed between its write and its publish, or while holding messages | Transactional outbox ([5](DECISIONS.md#5-events-are-published-through-a-transactional-outbox)); messages reappear after the visibility timeout; shutdown order ([10](DECISIONS.md#10-shutdown-follows-a-fixed-order)) | SIGKILL mid-run, then *accepted orders that finished* = accepted |
| Never charge twice | The same event delivered again | `events.Once` and unique constraints ([6](DECISIONS.md#6-consumers-record-the-events-they-have-handled)) | Seven *replayed … recognised as a duplicate* checks; *nothing changed after the replays* |
| Never charge for tickets that are gone | A payment retried after its hold expired | Payments checks `hold_expires_at` ([21](DECISIONS.md#21-a-hold-expires-and-an-order-is-never-charged-after-its-hold-has)) | Outage check: *payments charged after the hold had expired* = 0 |
| Never leave an order stuck | Payment provider down; the process that expires holds dies | Holds expire; scheduler with a standby ([20](DECISIONS.md#20-the-scheduler-only-keeps-time-and-one-replica-at-a-time-does-it), [21](DECISIONS.md#21-a-hold-expires-and-an-order-is-never-charged-after-its-hold-has)) | Outage check: *orders failed* = placed, *tickets back in stock* = capacity, standby leads after the kill |
| Never block the line | A message that can never be handled | Left for SQS to dead-letter after 3 receives ([7](DECISIONS.md#7-a-message-that-cannot-be-handled-is-left-for-the-dead-letter-queue)) | *poison message reached the dead-letter queue* |
| Tell each customer once | Many paths to the end of an order | `order.ticketed` / `order.failed` written with the status change ([22](DECISIONS.md#22-orders-announces-how-each-order-ended)) | *customers told how their order ended, once each*; outage check: *customers told a second time* = 0 |
| Open on time, without a person | Nobody presses a button at 10:00 | Scheduler → catalog → `drop.opened` → inventory creates stock ([19](DECISIONS.md#19-catalog-owns-a-drops-details-and-inventory-learns-them-when-it-opens)) | *an order placed before the opening time is refused*; *the drop went on sale N ms after its opening time* |

### How each check breaks the system

| Fault injected | How | Check |
|---|---|---|
| A service disappears mid-drop | `docker kill -s SIGKILL` on payments, inventory or fulfillment (no graceful shutdown, as when a node is lost) | drop-check, `KILL=…` |
| Demand exceeds supply | 600 orders of 1 to 4 tickets for 1,000 tickets, 16 at a time | drop-check |
| Duplicate delivery | Publishes again an event that was already delivered, at every hop | drop-check |
| A poison message | Sends a message no handler can parse | drop-check |
| Payment provider outage | Restarts payments with `PAYMENT_ERROR_RATE=1` | outage-check |
| Holds running out | Restarts inventory with 40-second holds | outage-check |
| Leader loss | SIGKILLs the scheduler replica holding the advisory lock | outage-check |
| Recovery after an outage | Moves the dead-lettered payments back onto the queue once the provider is back | outage-check |

### Not proven yet

- **A crowd larger than 4,000** on compose, or larger than 1,000 on the
  cluster. On the cluster 2,000 customers failed when Postgres ran out of
  connections
  ([TROUBLESHOOTING 9](TROUBLESHOOTING.md#9-orders-could-not-connect-to-postgres-at-2000-customers-on-the-cluster));
  the pools now have fixed sizes (DECISIONS 57), but the run has not been
  repeated, because port 8088 drops connections above about 1,024
  ([TROUBLESHOOTING 10](TROUBLESHOOTING.md#10-port-8088-closes-connections-above-about-1024-before-they-reach-traefik))
  and the cluster cannot be sent more than about 1,000 customers from this
  machine. At 4,000 on compose the next limit showing is fulfillment,
  throttled on purpose: orders took up to 75 s to end, against the 2 minutes a
  k6 customer waits.
- **Notifications under interruption.** "Told once" is asserted in runs where
  notifications itself is not killed; killing it may send a message twice, by
  design ([23](DECISIONS.md#23-notifications-keeps-no-record-so-a-message-can-be-sent-twice)).
- **The late `payment.succeeded`** that reaches inventory after its hold was
  released. Known, documented, not exercised by a check.
- **Kubernetes behaviour:** pod deletion mid-drop and rolling updates are not
  checked yet. Network policies (DECISIONS 49 to 51) and queue-depth scaling
  (15 November, below) have been proven on the local cluster only, and a new
  pod is not policed for about its first second (TROUBLESHOOTING 18).

---

## Earlier measurements

Recorded in DECISIONS.md when each decision was made.

| Measurement | Result | Source |
|---|---|---|
| 400 concurrent hold requests for 100 tickets | Exactly 100 holds granted | [DECISIONS 16](DECISIONS.md#16-stock-is-one-counter-per-tier-taken-with-one-conditional-update) |
| Time from opening time to orderable, three runs | 0.4 to 0.9 s | [DECISIONS 19](DECISIONS.md#19-catalog-owns-a-drops-details-and-inventory-learns-them-when-it-opens) |
| Scheduler standby takeover after the leader was killed | 5 s | [DECISIONS 20](DECISIONS.md#20-the-scheduler-only-keeps-time-and-one-replica-at-a-time-does-it) |
| Payment dead-lettered, then redriven before / after hold expiry | Order completes / payment refused without a charge | [DECISIONS 17](DECISIONS.md#17-a-declined-payment-and-a-provider-outage-are-handled-differently) |
| SIGTERM to fulfillment mid-work, before the shutdown fix | 20 orders with tickets, 12 marked `ticketed`, 8 events left in the outbox | [TROUBLESHOOTING 3](TROUBLESHOOTING.md#3-after-a-graceful-stop-some-orders-stayed-pending-although-their-tickets-existed) |
| The same, after the fix | Outbox empty; every order with tickets marked `ticketed` | Same |

---

## Results

### 19 November 2026: Phase 3 exit check, CI in both repos

Not a run of the system but of the checks that guard it: each condition of the
Phase 3 exit was shown by a pull request made to pass or to fail. App repo
`main` at `d8b7e1e` (PR #4 merged), GitOps repo at `phase-3-ci` `061d040`.
Both rulesets require `ci-ok` from GitHub Actions, with no bypass.

| PR | Change | Expected | Result | Evidence |
|---|---|---|---|---|
| ticketdrop #5 | A comment in `services/orders/cmd/main.go` | Only `build (orders)`; mergeable | `changes`, `go`, `build (orders)`, `ci-ok` passed; `CLEAN`. The first run failed `make vet` on the comment's formatting (TROUBLESHOOTING 19) | [`06-one-service-built.txt`](evidence/phase-3/06-one-service-built.txt) |
| ticketdrop #6 | A line in `docs/README.md` | No build; mergeable | `build` skipped, `ci-ok` passed; `CLEAN` | [`06-docs-only-ci-ok.txt`](evidence/phase-3/06-docs-only-ci-ok.txt) |
| ticketdrop #7 | A test that calls `t.Fatal` | Blocked | `go` failed (`--- FAIL: TestProofFailingTestBlocksMerge`), `ci-ok` failed; `BLOCKED` | [`fail-6-test-blocks-merge.txt`](evidence/phase-3/fail-6-test-blocks-merge.txt) |
| ticketdrop #8 | notifications' runtime base `alpine:3.16.0` | Blocked by Trivy | CVE-2022-37434 in zlib 1.2.12-r1 (CRITICAL, fixed in 1.2.12-r2); `build (notifications)` failed at the gate, `ci-ok` failed; `BLOCKED` | [`fail-6-critical-cve-blocks-merge.txt`](evidence/phase-3/fail-6-critical-cve-blocks-merge.txt) |
| local, GitOps | `terminationGracePeriodSecond` in the chart, `parentRef` in the HTTPRoute | `validate.sh` fails | 10 of 10 refused by kubeconform; `helm lint` passed the same typo | [`fail-7-kubeconform-misspelled-field.txt`](evidence/phase-3/fail-7-kubeconform-misspelled-field.txt) |
| ticketdrop-gitops #1 | The GitOps CI itself | Lint, install, check, policy probe pass | 16 of 16 `check.sh` checks, everything Available in 99 s, probe blocked from `ticketdrop`; `CLEAN` | [`08-kind-install-test.txt`](evidence/phase-3/08-kind-install-test.txt) |
| ticketdrop-gitops #2 | orders' `DATABASE_MAX_CONNS: "10x"` | Lint passes, install fails, blocked | lint passed; orders in `CrashLoopBackOff` ("is not an integer"), `rebuild.sh` timed out, `ci-ok` failed; `BLOCKED` | [`fail-8-chart-blocks-merge.txt`](evidence/phase-3/fail-8-chart-blocks-merge.txt) |

Every workflow's token is `contents: read`, every checkout keeps no
credential, and every `uses:` is a full commit SHA, which both repos enforce
([`00-actions-settings.txt`](evidence/phase-3/00-actions-settings.txt),
[`01-actions-pinned.txt`](evidence/phase-3/01-actions-pinned.txt)).
**Verdict: passed.**

### 17 November 2026: demo-drop on the cluster with batched holds

Machine: the Docker Desktop cluster, as in the run below, after the rebuilt
`inventory` image was loaded into the node and the Deployment restarted
(TROUBLESHOOTING 8): both pods on image `sha256:50ad279e...`, serving
`inventory_hold_batch_size`. Commits: `ticketdrop` `560b0f4` plus the load
test and batched holds, not yet committed; `ticketdrop-gitops` `e6e174e`.

| Run | Customers / tickets | Accepted | Sold out | Refused with an error | Buy time, median / p95 / max | Orders ended, p95 / max | Result |
|---|---|---|---|---|---|---|---|
| `load-1794918876` | 1,000 / 1,000 | 424 | 576 | 0 | 49 / 110 / 382 ms | 14.1 / 15.1 s | passed |
| `load-1794918941` | 2,000 / 2,000 | 741 | 1,144 | **115** | 122 / 950 / 1,183 ms | 38.8 / 40.5 s | **failed** |
| `load-1794921857` | 1,000 / 1,000, pools capped (entry 57) | 405 | 595 | 0 | 45 / 183 / 299 ms | 20.1 / 20.2 s | passed |

The 1,000 run is the one that failed twice on this cluster with one hold per
transaction (433 and 679 refused, below). With batches it passed: every check,
928 tickets issued, nothing left held, nothing dead-lettered. Its 1,000 hold
requests were answered in 342 transactions across the two pods
(`inventory_hold_batch_size`).

The 2,000 run failed two checks: 115 buys refused, and 2,000 tickets gone from
stock against 1,686 issued, so 314 held with no order. Neither was
`inventory`: `orders` logged 103 failures to connect to Postgres ("remaining
connection slots are reserved", "too many clients already") and answered
those buys with 503, after the hold had been granted
([TROUBLESHOOTING 9](TROUBLESHOOTING.md#9-orders-could-not-connect-to-postgres-at-2000-customers-on-the-cluster)).
The run was also not the crowd it was meant to be: 162,354 of k6's 188,490
requests (86 %) were polls of the drop page closed with `EOF` before reaching
Traefik, so many customers saw the drop open late and the crowd took 254 s
instead of about 30 s
([TROUBLESHOOTING 10](TROUBLESHOOTING.md#10-port-8088-closes-connections-above-about-1024-before-they-reach-traefik)).
Nothing was oversold.

The third run came after each service's pool was given a fixed size
(`DATABASE_MAX_CONNS`, entry 57; six images rebuilt, loaded into the node and
upgraded to Helm revision 3; `ticketdrop-gitops` values changed, not yet
committed). Every pod logged its pool size at startup. Postgres peaked at 65
client connections during the run (catalog 16, orders 18, inventory 16,
fulfillment 8, payments 4, scheduler 3), against 94 held idle after the 2,000
run. Catalog and inventory sat at their caps for most of it, and the p95 buy
time was 183 ms against 110 ms in the first run. `make drop-check` on compose,
with the same pool sizes, also passed.

Evidence: [`9-k6-1000-batched-holds-cluster.json`](evidence/phase-1/9-k6-1000-batched-holds-cluster.json),
[`10-k6-2000-batched-holds-cluster.json`](evidence/phase-1/10-k6-2000-batched-holds-cluster.json),
[`11-postgres-connections-exhausted.txt`](evidence/phase-1/11-postgres-connections-exhausted.txt),
[`12-port-8088-connection-ceiling.txt`](evidence/phase-1/12-port-8088-connection-ceiling.txt),
[`13-k6-1000-capped-pools-cluster.json`](evidence/phase-1/13-k6-1000-capped-pools-cluster.json),
[`14-postgres-connections-capped-pools.txt`](evidence/phase-1/14-postgres-connections-capped-pools.txt).

### 17 November 2026: demo-drop on compose, before and after batching holds

Machine: the compose stack on the same laptop, its own Postgres 18.6 (defaults:
`synchronous_commit` on). Commit: `ticketdrop` `560b0f4` plus the load test and
batched holds, not yet committed. "Before" is `inventory` built from `560b0f4`
(one hold per transaction); "after" is entry 56 (batches). All other services
and settings the same.

| Run | `inventory` | Customers / tickets | Accepted | Sold out | Refused with an error | Buy time, median / p95 / max | Orders ended, p95 / max | Result |
|---|---|---|---|---|---|---|---|---|
| `load-1794906558` | before | 1,000 / 1,000 | 424 | 576 | 0 | 500 / 1,132 / 1,262 ms | 19.0 / 20.1 s | passed |
| `load-1794906608` | before | 1,000 / 1,000 | 418 | 582 | 0 | 342 / 936 / 1,089 ms | 19.0 / 20.0 s | passed |
| `load-1794906667` | before | 2,000 / 2,000 | 544 | **0** | **1,456** | 2,004 / 2,013 / 2,046 ms | 26.1 / 27.1 s | **failed** |
| `load-1794906733` | after | 1,000 / 1,000 | 436 | 564 | 0 | 14 / 26 / 49 ms | 19.1 / 19.1 s | passed |
| `load-1794906787` | after | 2,000 / 2,000 | 813 | 1,187 | 0 | 14 / 33 / 51 ms | 36.1 / 37.1 s | passed |
| `load-1794906887` | after | 4,000 / 4,000 | 1,618 | 2,382 | 0 | 23 / 177 / 309 ms | 71.2 / 75.2 s | passed |

The failed "before" run also left 64 tickets held with no order (1,309 gone
from stock, 1,245 issued), as on the cluster. Batch sizes, from
`inventory_hold_batch_size`: 3,000 hold requests in 946 transactions over the
1,000 and 2,000 runs, and 4,000 in 374 over the 4,000 run. "Orders ended" grows
with the crowd because fulfillment is held at about 20 orders a second
(`SIMULATED_RENDER_TIME`, `CONSUMER_CONCURRENCY`), with one replica in compose.

Also passed with batching, before the load runs: a race of 400 single-ticket
orders for 100 tickets through the gateway (100 accepted, 300 sold out, 41
transactions), `make drop-check` for each `KILL` value, and `make outage-check`.

Evidence: [`6-k6-2000-one-hold-per-transaction.json`](evidence/phase-1/6-k6-2000-one-hold-per-transaction.json),
[`7-k6-2000-batched-holds.json`](evidence/phase-1/7-k6-2000-batched-holds.json),
[`8-k6-4000-batched-holds.json`](evidence/phase-1/8-k6-4000-batched-holds.json).

### 17 November 2026: demo-drop on the cluster, one hold per transaction

Machine: the Docker Desktop cluster, one node (16 CPUs), Postgres from
CloudNativePG (`synchronous_commit` on, `fsync` on), two pods each of web,
gateway, orders and inventory. Commits: `ticketdrop` `560b0f4` plus
`loadtest/drop.js` and `scripts/demo-drop.sh`, not yet committed;
`ticketdrop-gitops` `e6e174e`. k6 2.4.0 from its Docker image. Each customer
arrives within `ARRIVE` seconds, waits on the drop page (asking every second),
takes 0.5 to 3 s to fill the form after it opens, buys 1 to 4 tickets once,
then watches the order every second until it ends.

| Run | Customers / tickets | Accepted | Sold out | Refused with an error | Buy time, median / p95 | Holds granted per second, peak | Oversold | Result |
|---|---|---|---|---|---|---|---|---|
| `load-1794905322` | 200 / 200 | 93 | 107 | 0 | 6 ms / 23 ms | 72 | no (200 of 200) | passed |
| `load-1794905356` | 1,000 / 1,000 | 389 | 178 | **433** | 1,962 ms / 2,116 ms | 126 | no (900 issued) | **failed** |
| `load-1794905446` | 1,000 / 1,000 | 321 | **0** | **679** | 2,038 ms / 2,149 ms | 112 | no (713 issued) | **failed** |
| `load-1794906384` | 1,000 / 1,000 | 404 | 13 | **583** | 2,020 ms / 2,096 ms | | no (869 issued) | **failed** |
| `load-1794906444` | 1,000 / 1,000 | 406 | 272 | **322** | 1,098 ms / 1,916 ms | | no (938 issued) | **failed** |

The last two were meant to measure batched holds, but the pods restarted for
them still ran the old image
([TROUBLESHOOTING 8](TROUBLESHOOTING.md#8-a-rebuilt-dev-image-is-not-what-the-cluster-runs-even-after-a-restart)),
so they are two more runs of the old code. Each again left tickets held with
no order (32 and 3).

In both 1,000-customer runs every refused buy took about 2 s and ended with
`orders` logging `ask inventory for a hold: ... Client.Timeout exceeded`: its
hold call to `inventory` reached `INVENTORY_TIMEOUT` (2 s) and the customer was
shown "We could not reach the box office". Inventory's own histogram shows most
hold requests taking 1 to 2.5 s. No pod restarted and no request failed at the
HTTP level (k6 `http_req_failed` 0.00 %): every customer got an answer, about
half of them the wrong one.

**Where the time went.** Sampling `pg_stat_activity` in the `inventory` database
every 0.25 s during the second run
([`evidence/phase-1/4-hot-row-lock-waits.txt`](evidence/phase-1/4-hot-row-lock-waits.txt)):
up to 32 sessions active at once, which is both pods' whole connection pool,
and 200 of 220 samples were `UPDATE stock` waiting on `Lock/transactionid` or
`Lock/tuple`. The session holding the row was in `commit`
(`LWLock/WALWrite`, `IO/WalSync`). Every hold for a tier, granted or refused,
waits for the previous one to commit and flush. That caps one tier at about
110 to 130 granted holds a second on this machine: the bottleneck predicted in
the cost of [DECISIONS 16](DECISIONS.md#16-stock-is-one-counter-per-tier-taken-with-one-conditional-update).

**Two consequences seen.**

- Customers were turned away while tickets were left: the second run sold 713
  of 1,000 and told nobody "sold out".
- Holds with no order. In the first run 918 tickets left stock and 900 were
  issued: 18 tickets were held for requests whose caller had already given up,
  so no order exists for them. They return to stock when the hold expires, 10
  minutes and 30 seconds later, as the cost of
  [DECISIONS 15](DECISIONS.md#15-an-order-is-accepted-only-after-inventory-has-held-its-tickets)
  says. The check *tickets gone from stock equals tickets issued* failed on this.

Evidence: [`3-k6-1000-customers-summary.json`](evidence/phase-1/3-k6-1000-customers-summary.json)
(k6's summary of the first 1,000 run),
[`5-demo-drop-1000-second-run.txt`](evidence/phase-1/5-demo-drop-1000-second-run.txt)
(the second run's output).

### 15 November 2026: KEDA scales fulfillment 0 → N → 0 on queue depth

Machine: the Docker Desktop cluster, one node, Kubernetes v1.36.4, KEDA 2.21.0.
Commits: `ticketdrop` `d6e3467`, `ticketdrop-gitops` `436f8e4` plus the step 10
chart changes, not yet committed (fulfillment at Helm revision 5). Not a
`make` check: a script placed the orders through the public port 8088 and
sampled the queue, KEDA's metric and the replicas every 2 to 3 seconds.
Output: [10-fulfillment-scale-0-n-0.txt](evidence/phase-2/10-fulfillment-scale-0-n-0.txt);
the first scale to 0, when the ScaledObject was applied, is in
[10-scaledobject.txt](evidence/phase-2/10-scaledobject.txt).

Settings: `minReplicaCount` 0, `maxReplicaCount` 5, `queueLength` 100,
`activationQueueLength` 0, `pollingInterval` 5 s, `cooldownPeriod` 60 s,
scale-down stabilization 30 s. Load: 1,500 orders of one ticket for 1,500
tickets, 16 at a time. Times are local (KST).

| Assertion | Result |
|---|---|
| Fulfillment pods when the ScaledObject was applied to an empty queue | 1 → 0, in 6 s (01:11:34 to 01:11:40) |
| Orders accepted | 1,500 of 1,500 (`201`), placed in 10 s (01:17:57 to 01:18:07) |
| Orders start → first fulfillment pod created / Ready | 3 s / 6 s |
| Highest backlog seen (visible + in flight) | 243, at 01:18:08 |
| Highest replica count | 3 (HPA "above target"), all Ready 11 s after the orders started |
| Queue empty and no order pending | 01:18:29, 32 s after the orders started |
| Scale-down | 3 → 2 at 01:18:35, 2 → 1 at 01:18:50, 1 → 0 at 01:19:25 (KEDA) |
| Queue empty → no fulfillment pods | 61 s (01:18:29 to 01:19:30) |
| Orders ticketed / failed / pending | 1,355 / 145 / 0 |
| Tickets issued / back in stock | 1,355 / 145 |
| Messages in the five dead-letter queues | 0 before, 0 after |
| Operator log lines with `error` or `fail` | none |
| **Verdict** | **passed** |

#### Observations

- **It stopped at 3 of 5 replicas.** The backlog peaked at 243 against a target
  of 100 per pod, so the HPA asked for 3. The backlog is limited by how fast
  payments and inventory pass orders on, which was not measured; reaching 5
  needs a faster upstream or a lower `queueLength`.
- **Back to 0 took 61 s, not the 90 s of window plus cooldown.** The HPA's 30 s
  window and KEDA's 60 s cooldown run at the same time, from when the queue was
  last busy, so the last pod went at about the cooldown alone.
- **The 145 failed orders are declined payments** (`PAYMENT_DECLINE_RATE` 0.1),
  and their tickets went back to stock.


### 6 October 2026: drop-check, once per `KILL` value, then outage-check

Machine: a Linux cloud container, Docker 29. Commit: `3c43610`.
The images were not built by `make up`: Docker Hub was rate-limiting pulls of
the `golang` builder image, so each service was compiled on the host with the
same flags as its Dockerfile and copied into the same
`gcr.io/distroless/static-debian13:nonroot` base. The stack was then started
from those images with `docker compose up --no-build --wait`.

Settings: `ORDERS=600 CAPACITY=1000`, compose defaults otherwise (10% of
payments declined, 100 ms provider latency, fulfillment at 4 × 200 ms).

| | `KILL=payments` | `KILL=inventory` | `KILL=fulfillment` |
|---|---|---|---|
| Drop on sale after its opening time | 1,171 ms | 1,080 ms | 1,156 ms |
| Orders accepted / refused as sold out | 414 / 186 | 413 / 187 | 412 / 188 |
| Killed after it had handled | 155 orders | 151 orders | 48 orders |
| Every order finished, after the restart | 14 s | 14 s | 17 s |
| Ticketed + failed = accepted | 368 + 47 = 415 | 383 + 31 = 414 | 370 + 43 = 413 |
| Tickets issued (capacity 1,000) | 914 | 951 | 917 |
| Orders with the wrong number of tickets | 0 | 0 | 0 |
| Gone from stock = issued | yes | yes | yes |
| Holds left waiting / released = failed | 0 / yes | 0 / yes | 0 / yes |
| Dead-lettered during the run | 0 | 0 | 0 |
| Customers told, once each | 415 | 414 | 413 |
| Seven replayed events, all recognised as duplicates; state unchanged | yes | yes | yes |
| Poison message in the dead-letter queue after | 34 s | 34 s | 34 s |
| **Verdict** | **passed** | **passed** | **passed** |

(Accepted counts include the one order placed through the shopfront.)

**outage-check**, default settings (5 orders for 20 tickets, holds of 40 s,
provider failing every call):

| Assertion | Result |
|---|---|
| Orders accepted while the provider was down | 5 of 5 (12 of 20 tickets held) |
| Scheduler standby took over after the leader was SIGKILLed | 1 s; exactly one leader, not the one killed |
| Every order failed, reason "hold expired" | 5 of 5, 47 s after being placed |
| Holds released / tickets back in stock / tickets issued | 5 / 20 of 20 / 0 |
| Payments dead-lettered during the outage | 5 |
| Customers told their order failed | 5 |
| Dead-lettered payments redriven after the provider recovered | 5 |
| Refused without a charge, because the hold had expired | 5 |
| Charged after the hold had expired | 0 |
| Orders, holds, stock and tickets changed by the redrive | no |
| Customers told a second time / new dead letters | 0 / 0 |
| Both scheduler replicas running again | yes |
| **Verdict** | **passed** |

#### Observations

- **Tickets came back after buyers had been told "sold out".** In every run,
  about 187 orders were refused as sold out within the first three seconds, yet
  the drop ended with 49 to 86 tickets unsold. Those are the tickets of orders
  whose payment was declined (10% of payments here): they returned to stock
  seconds after the refusals. The system is correct, since nothing was oversold
  and nothing was stuck, but a real drop would lose those sales. A waitlist, or
  telling refused buyers to try again shortly, would recover them. It is not a
  recorded decision yet.
- **Opening took longer than recorded before:** 1.1 to 1.2 s here against 0.4 to
  0.9 s in the runs behind DECISIONS 19. The delay is made of the scheduler's
  one-second tick, the 250 ms outbox interval and delivery to inventory, so a
  delay of a little over a second is within design. DECISIONS 19 now carries
  both ranges.
- **The standby took over in 1 s**, against 5 s in the run behind DECISIONS 20.
  `LEADER_RETRY` (5 s) is the upper bound: how soon a standby tries the lock
  depends on where it was in its retry interval when the leader died.

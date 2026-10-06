# Proof

What the system promises, what could break each promise, how it is broken on
purpose, and what happened when it was. Unit tests cover the pieces; this page
is about the system as a whole, running.

Two scripts do the work, against the local stack started by `make up`:

- `make drop-check` (`scripts/drop-check.sh`), about two minutes. Run it once for
  each value of `KILL` after changing anything on the event path.
- `make outage-check` (`scripts/outage-check.sh`), about two minutes. Run it after
  changing holds, payments or the scheduler.

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

- **A real crowd.** The checks send 16 customers at a time. Behaviour at
  thousands of concurrent buyers, and where the first bottleneck is, are
  unmeasured.
- **Notifications under interruption.** "Told once" is asserted in runs where
  notifications itself is not killed; killing it may send a message twice, by
  design ([23](DECISIONS.md#23-notifications-keeps-no-record-so-a-message-can-be-sent-twice)).
- **The late `payment.succeeded`** that reaches inventory after its hold was
  released. Known, documented, not exercised by a check.
- **Kubernetes behaviour:** pod deletion mid-drop, rolling updates, probes,
  network policies and queue-depth scaling arrive with Phase 2.

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

# Talking about TicketDrop

How to explain this project in an interview, using what the repository can back
up. Every story here links to the record, so any answer can be followed with
"I can show you".

The shape of every answer is the same: **the problem, the choice and why, what
broke, what I changed, and what I would do at a different scale.**

---

## The 30-second version

> I built a ticketing platform for timed drops, where thousands of people try
> to buy a few hundred tickets in the same second. It is nine Go services on
> Postgres, talking through SNS and SQS with a transactional outbox. The goal
> was that it never oversells, never loses an order and never charges twice,
> even while services are being killed. I proved that with an end-to-end check
> that sells out a drop and SIGKILLs a service in the middle, then compares
> every database. It found a real shutdown bug, which I fixed. Every decision
> and its cost is written down in the repo.

## The two-minute version

1. **Problem.** A drop is a burst: far more buyers than tickets, all at once. It
   has to stay correct while services crash and messages are delivered twice.
2. **Approach.** A database per service, events through an outbox to SNS and
   one SQS queue per consumer, idempotent handlers. Overselling is prevented by
   one conditional `UPDATE` on the stock row, not by a read and a write.
   Holds expire, so an outage cannot lock tickets for ever.
3. **Test and break.** `make drop-check` places 600 orders for 1,000 tickets,
   SIGKILLs payments, inventory or fulfillment mid-run, and asserts that nothing
   was oversold or lost, stock adds up, and replayed events change nothing.
   `make outage-check` takes the payment provider down and kills the scheduler's
   leader.
4. **What broke.** A *graceful* shutdown of fulfillment left orders `pending`
   although their tickets existed: handlers that finished during shutdown wrote
   events after the outbox relay had stopped. Exit code 0, no errors.
5. **What I changed.** Workers stop in reverse order of registration and the
   relay makes a final pass. Shutdown order became a recorded decision, with a
   rule for Kubernetes' grace period.
6. **At a different scale.** Smaller, I would build one service and keep the
   correctness pieces. Larger, the first limits are the single stock row per
   tier and the shopfront's polling; much larger, a virtual waiting room.

---

## Stories

Each one is short enough to tell in a minute and links to the full record.

### "How do you stop two people buying the last ticket?"

- **Problem.** Hundreds of concurrent requests for the same tier.
- **Choice.** One statement: `UPDATE stock SET available = available - n WHERE
  available >= n`, plus a `CHECK (available >= 0)`. The database decides under
  one row lock. I rejected read-then-write (a race) and a Redis counter in front
  (two stores that must agree), until a load test shows I need it.
- **Evidence.** 400 concurrent requests for 100 tickets were granted exactly
  100 holds. Every `make drop-check` run asserts tickets issued ≤ capacity and
  that each ticketed order holds exactly its quantity.
- **Cost and scale.** Every hold for a tier queues on one row. The next step is
  splitting the stock across several rows, then a waiting room.
- **Record.** [DECISIONS 16](DECISIONS.md#16-stock-is-one-counter-per-tier-taken-with-one-conditional-update),
  [SCALING](SCALING.md#1-the-single-stock-row-per-tier).

### "Tell me about a bug you found."

- **Problem.** After a clean SIGTERM, 20 orders had tickets but only 12 were
  `ticketed`. Eight events sat in fulfillment's outbox until it started again.
- **Cause.** All workers stopped at once; handlers that were finishing wrote
  events after the relay had gone.
- **Fix.** Stop workers one at a time, last registered first; register the
  relay first; give it a bounded final pass.
- **What I took from it.** Exit code 0 is not proof of correctness. The outbox
  meant nothing was lost, only late, which is why I trust the pattern. And the
  shutdown sequence now sets a rule for Kubernetes:
  `terminationGracePeriodSeconds > SHUTDOWN_DELAY + SHUTDOWN_TIMEOUT`.
- **Record.** [TROUBLESHOOTING 3](TROUBLESHOOTING.md#3-after-a-graceful-stop-some-orders-stayed-pending-although-their-tickets-existed),
  [DECISIONS 10](DECISIONS.md#10-shutdown-follows-a-fixed-order).

### "How do you handle a third-party outage?"

- **Problem.** The payment provider goes down mid-sale.
- **Choice.** A decline is an answer (fail the order, return the tickets). An
  error is a fault (retry, then dead-letter). Holds expire after 10 minutes plus
  30 seconds' grace, so the tickets come back even if nobody acts. Payments
  refuses to charge once a hold has expired, so redriving the dead-letter queue
  later cannot charge for tickets that were resold.
- **Evidence.** `make outage-check` proves the whole sequence, including that
  the redriven payments charge nobody.
- **The gap I left on purpose.** A charge that succeeds just before expiry but
  arrives more than 30 seconds late goes to the DLQ for a manual refund. It is
  documented, rare, and the fix (a refund event) is known.
- **Record.** [DECISIONS 17](DECISIONS.md#17-a-declined-payment-and-a-provider-outage-are-handled-differently),
  [21](DECISIONS.md#21-a-hold-expires-and-an-order-is-never-charged-after-its-hold-has).

### "How do you run a scheduled job with more than one replica?"

- **Choice.** Two scheduler replicas; the one holding a Postgres advisory lock
  leads. The lock belongs to the database session, so a killed leader releases
  it with no cleanup. More importantly, the scheduler does no work: it calls
  endpoints that are safe to call twice at once, so two leaders for a moment
  cost a wasted call, not a double effect.
- **Evidence.** The standby took over 1 to 5 seconds after the leader was
  SIGKILLed, depending on where it was in its 5-second retry interval.
- **What I would change.** On Kubernetes, a `Lease` would remove the need for a
  database. And a connection pooler in transaction mode would break the
  advisory lock, which is worth knowing before adding one.
- **Record.** [DECISIONS 20](DECISIONS.md#20-the-scheduler-only-keeps-time-and-one-replica-at-a-time-does-it).

### "When did you break your own rule?"

- **Rule.** Every consumer is idempotent: it records the event ID in the same
  transaction as its work.
- **Exception.** Notifications keeps no record, so a redelivered event sends
  the email twice. Sending an email cannot be in a database transaction, so a
  record could not prevent the repeat anyway, and a duplicate email is a
  nuisance rather than a loss. It passes the event ID as the message ID, so a
  provider with idempotency keys can drop the repeat.
- **Record.** [DECISIONS 23](DECISIONS.md#23-notifications-keeps-no-record-so-a-message-can-be-sent-twice).

### "How do you choose tools?"

- **Example.** LocalStack began requiring an account and token in March 2026.
  Before switching to goaws I tested it for exactly what the project relies on:
  filtered fan-out, raw delivery, long polling, redrive, and the queue-depth
  attributes KEDA reads. The test found that its receive count is wrong, so no
  code depends on it.
- **Record.** [DECISIONS 13](DECISIONS.md#13-goaws-is-the-local-sns-and-sqs-emulator).

### "Why so many services?"

- **Honest answer.** At this size, it does not need them. One service and one
  database with the same conditional update would be correct. I split it to
  practise the problems that only exist between services: partial failure,
  at-least-once delivery, shutdown mid-work, scaling per consumer. The
  boundaries follow ownership (stock, money, tickets, the order, the drop), so
  they are the ones I would keep if a real team grew into them.
- **Record.** [CASE_STUDY](CASE_STUDY.md#why-nine-services-for-a-system-this-size),
  [SCALING](SCALING.md#smaller-what-i-would-build-for-one-venue).

---

## Numbers worth remembering

All measured on the local stack. [PROOF.md](PROOF.md) has the runs they come from.

| Number | What it shows |
|---|---|
| 400 concurrent requests for 100 tickets → exactly 100 holds | The conditional update holds under contention |
| 600 orders for 1,000 tickets, a service SIGKILLed mid-run → 0 oversold, 0 lost, 0 duplicates | The promises hold through a crash; passed with payments, inventory and fulfillment each killed |
| 14–17 s | From restarting the killed service to every order finished |
| 1–5 s | Scheduler standby takeover after the leader was killed (bounded by `LEADER_RETRY`) |
| 0.4–1.2 s | How long after its opening time a drop becomes orderable |
| 3 attempts | Before a poison message is set aside in the dead-letter queue |
| 24 | Decisions recorded, each with its cost |

---

## Questions the record does not answer yet

An interviewer is likely to ask these, and the repository has no written
decision for them. Each is worth deciding and adding to
[DECISIONS.md](DECISIONS.md) in my own words before it is asked:

- **Why SNS and SQS, and not Kafka, EventBridge or RabbitMQ?** DECISIONS 4 explains
  one queue per consumer, but not why not a log-based broker with consumer groups.
- **Why choreography, and not an orchestrated saga** (Step Functions, Temporal)?
  The order's progress is spread across four services; an orchestrator would put
  it in one place.
- **What are the service-level objectives?** For example, the share of orders
  that reach a final state within some time, or hold latency at p99.
- **How would a schema change that is not backwards-compatible be rolled out?**
  DECISIONS 11 requires compatibility with the previous release, which implies
  expand-and-contract migrations, but does not say so.
- **What does it cost to run on AWS** for a drop, and when idle?
- **How are secrets handled on AWS?** The field guide plans EKS Pod Identity; it
  is not yet a recorded decision.
- **Why Go?**
- **What happens to buyers refused as sold out when tickets come back?** In
  every drop-check run, tickets from declined payments returned to stock after
  about 187 buyers had been told "sold out", and 49 to 86 tickets went unsold
  ([PROOF.md](PROOF.md#observations)). A waitlist, or a "try again shortly"
  answer, are the options.

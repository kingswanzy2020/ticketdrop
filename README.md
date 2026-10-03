# TicketDrop

A ticket-drop platform built as nine Go services that coordinate through events. When a drop opens, far more people order than there are tickets. TicketDrop sells exactly the capacity, never oversells, and leaves no order stuck, even when a service is killed mid-run.

Each service has its own Postgres database. Services talk through an outbox, SNS topic and per-consumer SQS queue, apart from one synchronous stock-hold call from orders to inventory and the scheduler's timer calls.

| Service | Role |
|---|---|
| web | Server-rendered shopfront (Go templates + htmx) |
| gateway | Public API reverse proxy with an explicit route allowlist |
| catalog | Drops, tiers, prices, capacities; publishes `drop.opened` |
| orders | Takes orders, tracks them to `ticketed` or `failed` |
| inventory | Owns stock; holds tickets with one conditional `UPDATE` |
| payments | Charges via a simulated provider |
| fulfillment | Issues tickets |
| notifications | Tells the customer the outcome |
| scheduler | Leader-elected clock that opens drops and expires holds |

## Run it locally

Requires Go, Docker with Compose, and for the checks `curl`, `jq` and the `aws` CLI.

```bash
make up          # build and start the stack; shopfront on http://localhost:8092
make test        # unit tests (no Postgres or emulator needed)
make vet         # go vet + gofmt check
make drop-check  # oversell / loss / duplicate proof against the running stack
make outage-check
make down        # stop and delete volumes
```

Run `make help` for all targets. SNS and SQS are emulated locally by goaws.

## Documentation

- [CLAUDE.md](CLAUDE.md): architecture and conventions in detail
- [docs/DECISIONS.md](docs/DECISIONS.md): each design choice, its reason and cost
- [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md): failures that actually happened and how they were diagnosed
- [docs/diagrams/](docs/diagrams/): architecture diagrams

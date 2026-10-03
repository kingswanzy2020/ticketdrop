COMPOSE := docker compose -f deploy/local/compose.yaml

.DEFAULT_GOAL := help

.PHONY: help
help: ## List the targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

.PHONY: test
test: ## Run the unit tests
	go test ./...

.PHONY: vet
vet: ## Run go vet and check formatting
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "not gofmt-formatted:"; gofmt -l .; exit 1; }

.PHONY: up
up: ## Build and start the local stack (shopfront on http://localhost:8092, API on :8090)
	$(COMPOSE) up -d --build --wait

.PHONY: down
down: ## Stop the local stack and delete its data
	$(COMPOSE) down -v --remove-orphans

.PHONY: ps
ps: ## Show the local stack's containers
	$(COMPOSE) ps

.PHONY: logs
logs: ## Follow the services' logs
	$(COMPOSE) logs -f --tail=50 web gateway catalog orders inventory payments fulfillment notifications scheduler

.PHONY: drop-check
drop-check: ## Run a drop and prove nothing is oversold, lost or duplicated (ORDERS=600 CAPACITY=1000 KILL=payments)
	ORDERS=$(ORDERS) CAPACITY=$(CAPACITY) KILL=$(KILL) ./scripts/drop-check.sh

.PHONY: outage-check
outage-check: ## Take the payment provider down and prove no order or ticket is left stuck (about 2 minutes)
	ORDERS=$(ORDERS) ./scripts/outage-check.sh

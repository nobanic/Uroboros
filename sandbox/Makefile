# Uroboros sandbox — convenience targets.
# Run from this directory:  cd sandbox && make <target>
#
# Two stacks, both publishing the UI on 127.0.0.1:8787, so run ONE at a time:
#   make sandbox    # sealed: no internet, no host
#   make internet   # secured, WITH public internet (host/LAN still blocked)

COMPOSE_SEALED := docker compose -f docker-compose.sandbox.yml --env-file .env
COMPOSE_NET    := docker compose -f docker-compose.internet.yml --env-file .env
UI             := http://127.0.0.1:8787

.DEFAULT_GOAL := help
.PHONY: help sandbox internet build-sealed build-internet \
        verify verify-sealed verify-internet \
        down down-sealed down-internet clean clean-sealed clean-internet \
        logs logs-sealed logs-internet ps open attach-target detach-target

help: ## Show this help
	@echo "Uroboros sandbox targets:"
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make sandbox                       # bring up the sealed stack + build"
	@echo "  make verify                        # prove the seal"
	@echo "  make attach-target TARGET=monixa   # attach your container as monixa.local"
	@echo "  make internet                      # bring up the internet-enabled stack"

## --- Setup -------------------------------------------------------------------
.env: ## Generate a local .env with a random Postgres password (auto-run as needed)
	@printf '# Local only; the DB password never leaves the sealed network.\nPOSTGRES_PASSWORD=%s\n\n# Optional, internet variant only:\nANTHROPIC_API_KEY=\nOPENAI_API_KEY=\n' \
	  "$$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 40)" > .env
	@chmod 600 .env
	@echo "generated sandbox/.env with a random POSTGRES_PASSWORD"

## --- Sealed (offline) stack --------------------------------------------------
sandbox: .env ## Build + start the SEALED stack (no internet, no host), then show next steps
	$(COMPOSE_SEALED) up -d --build
	@echo ""
	@echo "Sealed stack up.  UI: $(UI)   (first visit -> /setup)"
	@echo "Verify the seal:  make verify"
	@echo "Attach a target:  make attach-target TARGET=<container-name>"

build-sealed: ## Build the sealed stack images only (no start)
	$(COMPOSE_SEALED) build

verify: verify-sealed ## Prove the seal against the running sealed stack
verify-sealed:
	./verify-isolation.sh

attach-target: ## Attach your target container to the sealed lab (TARGET=name [ALIAS=monixa.local])
	@test -n "$(TARGET)" || { echo "usage: make attach-target TARGET=<container-name> [ALIAS=monixa.local]"; exit 1; }
	./attach-target.sh "$(TARGET)" $(ALIAS)

detach-target: ## Remove your target container from the sealed lab (TARGET=name)
	@test -n "$(TARGET)" || { echo "usage: make detach-target TARGET=<container-name>"; exit 1; }
	./detach-target.sh "$(TARGET)"

down-sealed: ## Stop the sealed stack (keep data)
	$(COMPOSE_SEALED) down

clean-sealed: ## Stop the sealed stack AND delete its data volumes
	$(COMPOSE_SEALED) down -v

logs: logs-sealed ## Follow sealed-stack logs
logs-sealed:
	$(COMPOSE_SEALED) logs -f

## --- Internet-enabled stack --------------------------------------------------
internet: .env ## Build + start the INTERNET stack (public egress; host/LAN blocked), then show next steps
	$(COMPOSE_NET) up -d --build
	@echo ""
	@echo "Internet stack up.  UI: $(UI)   (first visit -> /setup)"
	@echo "Set an LLM key in the UI or sandbox/.env to run explorations."
	@echo "Verify egress policy:  make verify-internet"

build-internet: ## Build the internet stack images only (no start)
	$(COMPOSE_NET) build

verify-internet: ## Prove the internet variant (public OK, host/LAN blocked, DB sealed)
	./verify-internet.sh

down-internet: ## Stop the internet stack (keep data)
	$(COMPOSE_NET) down

clean-internet: ## Stop the internet stack AND delete its data volumes
	$(COMPOSE_NET) down -v

logs-internet: ## Follow internet-stack logs
	$(COMPOSE_NET) logs -f

## --- Shared ------------------------------------------------------------------
ps: ## Show sandbox containers
	@docker ps --filter "label=com.docker.compose.project=uroboros-sandbox" \
	           --filter "label=com.docker.compose.project=uroboros-internet" \
	           --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'

open: ## Open the UI in a browser (macOS)
	@open $(UI) 2>/dev/null || echo "Open $(UI) in your browser"

down: down-sealed down-internet ## Stop both stacks

clean: clean-sealed clean-internet ## Stop both stacks and delete all data volumes

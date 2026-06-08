TOOL_DIR  := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
REPO_ROOT := $(abspath $(TOOL_DIR)/../../..)
# Archived single-site tool — reports stay co-located under the tool
# folder (see ../../tools/archived/integration-suite/STALE.md).
REPORT_PATH := $(TOOL_DIR)/reports/last-run.md

NATS_CONTAINER := chat-local-nats

.PHONY: help validate local run preflight preflight-containers merge-performance

help:
	@echo "make -C tools/archived/integration-suite <target>"
	@echo "  validate                          — run the catalog validator (no live infra needed)"
	@echo "  local                             — run against the docker-local stack (assumes 'make up' is running)"
	@echo "  USE_INFRA=true make local         — boot the full stack programmatically (skips host-port preflight)"
	@echo "  run                               — alias for local; honors any env-var overrides"
	@echo "  INTERACTIVE=true make run         — open the stdin-driven scenario menu (Phase 4.6)"

validate:
	@cd $(TOOL_DIR) && go run ./cmd/validator

# Conditional preflight dependencies. The default `local` target assumes
# the docker-local stack is up on the host's fixed ports (8080, 4222,
# 27017, ...) and validates that via `preflight` + `preflight-containers`.
# USE_INFRA=true boots its own stack via internal/infra on random
# Testcontainers-assigned ports, so the static host-port checks would
# trip every time and the catalog-declared container names ('room-service',
# 'room-worker') don't exist at host-name resolution either. Empty the
# dependency list under USE_INFRA=true so the runner can boot its own
# world without arguing with preflight.
LOCAL_DEPS := preflight preflight-containers
ifeq ($(USE_INFRA),true)
LOCAL_DEPS :=
endif

local: $(LOCAL_DEPS)
	@cd $(TOOL_DIR) && \
		AUTH_SERVICE_URL=http://localhost:8080 \
		NATS_URL=nats://localhost:4222 \
		NATS_CREDS_FILE=$(REPO_ROOT)/docker-local/backend.creds \
		MONGO_URI=mongodb://localhost:27017 \
		MONGO_DB=chat \
		VALKEY_ADDR=localhost:6379 \
		SITE_ID=site-local \
		OUTPUT_PATH=$(REPORT_PATH) \
		APPROVED_OUTPUT_PATH=$(TOOL_DIR)/reports/last-run-approved.md \
		INTERACTIVE_OUTPUT_PATH=$(TOOL_DIR)/reports/last-run-interactive.md \
		PERFORMANCE_PATH=$(TOOL_DIR)/reports/performance.json \
		REPO_ROOT=$(REPO_ROOT) \
		go run ./cmd/runner

run: local

# merge-performance: invoke the perfmerge subcommand to deterministically
# merge two performance.json files (§5.4). Used as a git custom merge
# driver or manually. Usage:
#   make -C tools/integration-suite merge-performance \
#       LEFT=ours.json RIGHT=theirs.json OUT=merged.json
merge-performance:
	@cd $(TOOL_DIR) && go run ./cmd/perfmerge -left $(LEFT) -right $(RIGHT) -out $(OUT)

preflight:
	@docker container inspect -f '{{.State.Running}}' $(NATS_CONTAINER) 2>/dev/null | grep -q true || { \
	  echo "ERROR: $(NATS_CONTAINER) is not running. Run 'make deps-up && make up' from repo root."; exit 1; \
	}
	@curl -fsS http://localhost:8080/healthz >/dev/null 2>&1 || { \
	  echo "ERROR: auth-service not reachable on :8080. Is 'make up' running?"; exit 1; \
	}

preflight-containers:
	@count=$$(grep -h '^container:' $(TOOL_DIR)/catalogs/services/*.yaml 2>/dev/null | wc -l); \
	if [ "$$count" = "0" ]; then \
	  echo "preflight: no service catalogs declare a container: field yet (dormant)"; \
	else \
	  for c in $$(grep -h '^container:' $(TOOL_DIR)/catalogs/services/*.yaml | awk '{print $$2}'); do \
	    docker inspect -f '{{.Name}}' $$c > /dev/null 2>&1 || { \
	      echo "preflight: container '$$c' (declared in catalogs/services/) is not present in docker ps"; \
	      exit 1; \
	    }; \
	  done; \
	  echo "preflight: $$count declared container(s) all present"; \
	fi

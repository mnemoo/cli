# Makefile — dev convenience targets for the stakecli demo.
#
# Most common workflow for recording a demo:
#   make demo-build       # one-time: compile stakecli + mockapi + bundle
#   make mockapi          # terminal 1: run the mock API
#   make demo-run         # terminal 2: launch TUI (login screen every run)
#   make demo-reset       # between takes: wipe mock state
#
# Quick non-interactive cut (skips login):
#   make demo-upload

GO            ?= go
MOCKAPI_PORT  ?= 8080
MOCKAPI_ADDR  ?= :$(MOCKAPI_PORT)
MOCKAPI_URL   ?= http://localhost:$(MOCKAPI_PORT)
THROUGHPUT    ?= 3MiB
DEMO_SID      ?= demo
BUNDLE_DIR    ?= testdata/demo-bundle
DEMO_CONFIG   ?= /tmp/stakecli-demo
BIN_DIR       ?= bin

# Env bundle that ALL demo targets share.
DEMO_ENV = STAKE_API_URL=$(MOCKAPI_URL) \
           STAKE_NO_UPDATE_CHECK=1 \
           STAKE_CONFIG_DIR=$(DEMO_CONFIG) \
           STAKE_KEYRING_DISABLE=1

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# -----------------------------------------------------------------------------
# Build
# -----------------------------------------------------------------------------

.PHONY: build
build: ## Build all binaries into $(BIN_DIR)/
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/stakecli ./cmd/stake
	$(GO) build -o $(BIN_DIR)/mockapi ./cmd/mockapi
	$(GO) build -o $(BIN_DIR)/demo-bundle ./cmd/demo-bundle

.PHONY: clean
clean: ## Remove built binaries, generated bundle, and demo config
	rm -rf $(BIN_DIR) $(BUNDLE_DIR) $(DEMO_CONFIG)

# -----------------------------------------------------------------------------
# Demo
# -----------------------------------------------------------------------------

.PHONY: demo-bundle
demo-bundle: ## Generate the math+front demo bundle at $(BUNDLE_DIR)
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/demo-bundle ./cmd/demo-bundle
	./$(BIN_DIR)/demo-bundle -out $(BUNDLE_DIR)

.PHONY: demo-build
demo-build: build demo-bundle ## Build everything and generate the demo bundle

.PHONY: mockapi
mockapi: ## Run the mock API (foreground)
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/mockapi ./cmd/mockapi
	./$(BIN_DIR)/mockapi -addr $(MOCKAPI_ADDR) -throughput $(THROUGHPUT)

.PHONY: demo-reset
demo-reset: ## Reset mutable mock state (between recording takes)
	@curl -sf -X POST $(MOCKAPI_URL)/__reset && echo "mockapi state reset"

.PHONY: demo-health
demo-health: ## Ping the mock API's health endpoint
	@curl -sf $(MOCKAPI_URL)/__health

.PHONY: demo-logout
demo-logout: ## Wipe the isolated demo config (force login on next run)
	@rm -rf $(DEMO_CONFIG) && echo "demo config cleared: $(DEMO_CONFIG)"

.PHONY: demo-run
demo-run: ## Launch stakecli TUI with login screen (the full demo pipeline)
	@mkdir -p $(BIN_DIR) $(DEMO_CONFIG)
	@$(GO) build -o $(BIN_DIR)/stakecli ./cmd/stake
	@$(DEMO_ENV) ./$(BIN_DIR)/stakecli

.PHONY: demo-fast
demo-fast: ## Launch TUI with STAKE_SID bypass (skips login — for quick testing)
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/stakecli ./cmd/stake
	@$(DEMO_ENV) STAKE_SID=$(DEMO_SID) ./$(BIN_DIR)/stakecli

.PHONY: demo-shell
demo-shell: ## Spawn a shell with demo env vars exported (no STAKE_SID — TUI shows login)
	@echo "Spawning shell with:"
	@echo "  STAKE_API_URL=$(MOCKAPI_URL)"
	@echo "  STAKE_CONFIG_DIR=$(DEMO_CONFIG)"
	@echo "  STAKE_KEYRING_DISABLE=1"
	@mkdir -p $(DEMO_CONFIG)
	@$(DEMO_ENV) $$SHELL

.PHONY: demo-upload
demo-upload: ## Run a non-interactive upload of the demo math bundle (CLI path, bypasses login)
	@mkdir -p $(BIN_DIR)
	@$(GO) build -o $(BIN_DIR)/stakecli ./cmd/stake
	@$(DEMO_ENV) STAKE_SID=$(DEMO_SID) \
		./$(BIN_DIR)/stakecli upload \
			--team neon-labs --game cyber-samurai \
			--type math --path $(BUNDLE_DIR)/math \
			--yes --publish

.PHONY: demo
demo: demo-build ## Build everything and print next steps
	@echo ""
	@echo "All set. Open two terminals:"
	@echo "  1) make mockapi"
	@echo "  2) make demo-run          # full pipeline from login"
	@echo "     make demo-upload       # non-interactive CLI cut"
	@echo ""
	@echo "Reset state between takes:"
	@echo "  make demo-reset"

# -----------------------------------------------------------------------------
# Checks (used by CI too)
# -----------------------------------------------------------------------------

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: test
test: ## Run unit tests
	$(GO) test ./...

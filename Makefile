# mymac — build helpers

BINARY      := mymac
CMD         := ./cmd/mymac
PREFIX      ?= $(HOME)/.local/bin
BUILD_DIR   := build
VERSION     := $(shell sed -n 's/.*Version = "\(.*\)".*/\1/p' internal/config/config.go | head -1)
LDFLAGS     := -s -w
GOFLAGS     ?= -mod=mod
export GOPROXY ?= off

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the mymac binary into build/
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(CMD)
	@echo "built $(BUILD_DIR)/$(BINARY) (v$(VERSION))"

.PHONY: run
run: ## Build and run mymac, opening the browser
	@mkdir -p $(BUILD_DIR)
	go run $(GOFLAGS) $(CMD)

.PHONY: test
test: ## Run the unit tests
	go test $(GOFLAGS) ./... -count=1

.PHONY: test-race
test-race: ## Run the unit tests with the race detector
	go test $(GOFLAGS) -race ./... -count=1

.PHONY: lint
lint: ## Vet and format-check the tree
	go vet ./...
	@out="$$(gofmt -l . 2>/dev/null)"; \
	if [ -n "$$out" ]; then echo "gofmt needed for:"; echo "$$out"; exit 1; fi
	@echo "lint ok"

.PHONY: fmt
fmt: ## Format the tree
	gofmt -w .

.PHONY: icons
icons: ## Regenerate the favicon / app icon assets
	go run $(GOFLAGS) ./tools/genicon

.PHONY: install
install: build ## Install the binary into $(PREFIX)
	@mkdir -p $(PREFIX)
	install -m 0755 $(BUILD_DIR)/$(BINARY) $(PREFIX)/$(BINARY)
	@echo "installed $(PREFIX)/$(BINARY)"

.PHONY: bundle
bundle: build ## Package mymac.app into build/
	@./scripts/make-app-bundle.sh

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf $(BUILD_DIR)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

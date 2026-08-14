.PHONY: dev build run check-config generate wire test lint build-prod tools sqlc \
        docker-up docker-down hardhat-install hardhat-up hardhat-seed \
        image image-login image-push

APP_BIN := ./bin/app
# The only configuration source. There is no environment fallback, so nothing
# here exports variables.
CONFIG := config.development.json

# Image coordinates. Override on the command line for a one-off:
#   make image-push VERSION=v1.0.0
IMAGE      ?= ghcr.io/gos0001/gomod-cryptopay
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PLATFORMS  ?= linux/amd64,linux/arm64

BUILD_ARGS := --build-arg VERSION=$(VERSION) \
              --build-arg COMMIT=$(COMMIT) \
              --build-arg BUILD_DATE=$(BUILD_DATE)

# The same stamps the image gets, so `./bin/app version` does not report "dev"
# from a working copy that is at a tag.
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

dev:
	@air

run:
	@go run ./cmd -config $(CONFIG)

check-config:
	@go run ./cmd -check-config -config $(CONFIG)

build:
	@go build -ldflags "$(LDFLAGS)" -o $(APP_BIN) ./cmd

# sqlc MUST run before wire: wire cannot compile the postgres adapter until the
# generated/ package exists.
generate: sqlc wire

sqlc:
	@sqlc generate

wire:
	@wire ./cmd/

test:
	@go test ./... -race -count=1

lint:
	@golangci-lint run ./...

build-prod:
	@go build -tags production -ldflags "-s -w $(LDFLAGS)" -o $(APP_BIN) ./cmd

tools:
	@go install github.com/air-verse/air@latest
	@go install github.com/google/wire/cmd/wire@latest
	@go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

docker-up:
	@docker compose up -d postgres

docker-down:
	@docker compose down

# Single-arch build loaded into the local daemon. buildx cannot --load a
# multi-arch manifest, which is why building locally and pushing are separate
# targets rather than one target with a flag.
image:
	@docker buildx build $(BUILD_ARGS) -t $(IMAGE):$(VERSION) -t $(IMAGE):dev --load .
	@echo "built $(IMAGE):$(VERSION)"

# GHCR authenticates with a personal access token carrying write:packages.
image-login:
	@echo $$GITHUB_TOKEN | docker login ghcr.io -u gos0001 --password-stdin

# Pushes only the version tag. `latest` and the semver aliases are owned by CI,
# which moves them on a release tag — a manual push must not be able to point
# `latest` at an unreviewed local build.
image-push:
	@docker buildx build $(BUILD_ARGS) --platform $(PLATFORMS) \
		-t $(IMAGE):$(VERSION) --push .
	@echo "pushed $(IMAGE):$(VERSION) for $(PLATFORMS)"

# The local EVM node the BSC watcher is developed against. Kept out of `test`
# on purpose: it needs Node, and an ordinary test run must not depend on that.
#
# A local node rather than a testnet because a testnet cannot be made to
# reorganise on demand, and `detected -> pending` is the branch that handles a
# payment being un-mined.
hardhat-install:
	@cd hardhat && npm install --no-fund --no-audit

hardhat-up:
	@cd hardhat && npx hardhat node

hardhat-seed:
	@cd hardhat && npx hardhat run scripts/deploy.js --network localhost \
		&& npx hardhat run scripts/transfer.js --network localhost

# There is no migrate target on purpose. schema/schema.sql is embedded in the
# binary and applied at startup under an advisory lock, so a fresh database
# needs nothing but `docker compose up`. Schema changes are edits to that file,
# and every one of them has to be idempotent — see its header.

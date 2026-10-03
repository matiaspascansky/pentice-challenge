# Booking Confirmation Service

# Configuración del servidor (ver README para la lista completa).
PORT              ?= 3000
DATA_FILE         ?= ./data/store.json
GUEST_WEBHOOK_URL ?= http://localhost:4000/notifications

# Configuración del dispositivo simulado.
GUEST_PORT  ?= 4000
SERVER_URL  ?= http://localhost:3000
# Flags de falla para la demo, por ejemplo:
#   make run-guest GUEST_FLAGS="--ack-loss-rate=0.5"
GUEST_FLAGS ?=

.DEFAULT_GOAL := help

.PHONY: help
help: ## Muestra esta ayuda
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: run-server
run-server: ## Levanta el servidor de reservas (:3000)
	PORT=$(PORT) DATA_FILE=$(DATA_FILE) GUEST_WEBHOOK_URL=$(GUEST_WEBHOOK_URL) \
		go run ./cmd/server

.PHONY: run-guest
run-guest: ## Levanta el dispositivo simulado (:4000). Acepta GUEST_FLAGS
	go run ./cmd/guest --port=$(GUEST_PORT) --server-url=$(SERVER_URL) $(GUEST_FLAGS)

.PHONY: test
test: ## Corre los tests con el detector de carreras
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Corre los tests y reporta la cobertura total
	@go test -count=1 -coverpkg=./cmd/...,./internal/... -coverprofile=coverage.out ./... > /dev/null
	@go tool cover -func=coverage.out | tail -1
	@echo "detalle: go tool cover -html=coverage.out"

.PHONY: check
check: ## Formato, vet y tests
	gofmt -l . | tee /dev/stderr | (! read)
	go vet ./...
	$(MAKE) test

.PHONY: build
build: ## Compila los dos binarios en ./bin
	go build -o bin/server ./cmd/server
	go build -o bin/guest ./cmd/guest

.PHONY: clean
clean: ## Borra binarios, cobertura y el estado persistido
	rm -rf bin coverage.out $(dir $(DATA_FILE))

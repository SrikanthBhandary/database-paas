SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

DATABASE_URL ?= postgres://paas:paas@localhost:5432/paas?sslmode=disable
GOOSE        ?= go run github.com/pressly/goose/v3/cmd/goose@latest
MIGRATIONS   ?= deploy/migrations
COMPOSE ?= docker compose -f deploy/docker-compose.yml


# ---- config ----
CLUSTER_NAME          ?= dbpaas
KIND_CONFIG           ?= deploy/kind/kind-config.yaml
CERT_MANAGER_VERSION  ?= v1.20.0
BARMAN_PLUGIN_VERSION ?= v0.14.0
TEST_NS               ?= tenant-test
PORT                  ?= 8000

.DEFAULT_GOAL := help

.PHONY: help
help: ## show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---- local cluster ----
.PHONY: cluster
cluster: ## create the kind cluster (skips if it exists)
	@kind get clusters | grep -qx '$(CLUSTER_NAME)' || \
		kind create cluster --config $(KIND_CONFIG)
	@kubectl config use-context kind-$(CLUSTER_NAME)

.PHONY: cert-manager
cert-manager: ## install cert-manager (needed by the Barman plugin)
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml
	kubectl rollout status deploy/cert-manager -n cert-manager --timeout=180s
	kubectl rollout status deploy/cert-manager-webhook -n cert-manager --timeout=180s
	kubectl rollout status deploy/cert-manager-cainjector -n cert-manager --timeout=180s

.PHONY: cnpg
cnpg: ## install the CloudNativePG operator
	helm repo add cnpg https://cloudnative-pg.github.io/charts --force-update
	helm upgrade --install cnpg cnpg/cloudnative-pg \
		--namespace cnpg-system --create-namespace
	kubectl rollout status deploy/cnpg-cloudnative-pg -n cnpg-system --timeout=180s

.PHONY: barman-plugin
barman-plugin: ## install the Barman Cloud plugin into cnpg-system
	kubectl apply -f https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/$(BARMAN_PLUGIN_VERSION)/manifest.yaml
	kubectl rollout status deploy/barman-cloud -n cnpg-system --timeout=180s

.PHONY: minio
minio: ## deploy MinIO and create the cnpg-backups bucket
	kubectl apply -f deploy/kind/minio.yaml --selector='app!=none' 2>/dev/null || true
	kubectl -n minio delete job minio-create-bucket --ignore-not-found
	kubectl apply -f deploy/kind/minio.yaml
	kubectl rollout status deploy/minio -n minio --timeout=180s
	kubectl wait --for=condition=complete job/minio-create-bucket -n minio --timeout=120s

.PHONY: up
up: cluster cert-manager cnpg barman-plugin minio  db-up migrate-up ## build the whole environment
	@echo "environment ready"

.PHONY: down
down: ## delete the kind cluster
	kind delete cluster --name $(CLUSTER_NAME)

.PHONY: status
status: ## show operator, plugin, MinIO and test cluster state
	kubectl get pods -n cert-manager
	kubectl get pods -n cnpg-system
	kubectl get pods -n minio
	-kubectl get cluster,backup,scheduledbackup -n $(TEST_NS)

# ---- test tenant ----
.PHONY: test-tenant
test-tenant: ## create a test namespace, ObjectStore, Postgres cluster and schedule
	kubectl create namespace $(TEST_NS) --dry-run=client -o yaml | kubectl apply -f -
	kubectl -n $(TEST_NS) create secret generic minio-creds \
		--from-literal=ACCESS_KEY_ID=minioadmin \
		--from-literal=ACCESS_SECRET_KEY=minioadmin123 \
		--dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f deploy/kind/objectstore.yaml -f deploy/kind/test-cluster.yaml
	kubectl -n $(TEST_NS) wait --for=condition=Ready cluster/db-test --timeout=300s

.PHONY: test-backup
test-backup: ## trigger a manual backup of the test cluster
	@printf '%s\n' \
	  'apiVersion: postgresql.cnpg.io/v1' \
	  'kind: Backup' \
	  'metadata:' \
	  '  generateName: db-test-manual-' \
	  '  namespace: $(TEST_NS)' \
	  'spec:' \
	  '  cluster: {name: db-test}' \
	  '  method: plugin' \
	  '  pluginConfiguration: {name: barman-cloud.cloudnative-pg.io}' \
	  | kubectl create -f -
	kubectl -n $(TEST_NS) get backup

.PHONY: test-clean
test-clean: ## remove the test tenant
	kubectl delete namespace $(TEST_NS) --ignore-not-found

.PHONY: minio-console
minio-console: ## port-forward the MinIO console to localhost:9001
	kubectl -n minio port-forward svc/minio 9001:9001

# ---- Go ----
.PHONY: run
run: ## run the API server
	go run . -port $(PORT)

.PHONY: build
build: ## build the binary into bin/
	go build -o bin/db-paas .

.PHONY: test
test: ## run tests with the race detector
	go test -race -count=1 ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

# ---- control-plane database ----
PHONY: db-up
db-up: ## start the local Postgres and wait until it is healthy
	$(COMPOSE) up -d --wait postgres

.PHONY: db-down
db-down: ## stop the local Postgres (data is kept)
	$(COMPOSE) stop postgres

.PHONY: db-reset
db-reset: ## DESTROY local data, recreate, and migrate
	$(COMPOSE) down -v
	$(MAKE) db-up
	$(MAKE) migrate-up

.PHONY: db-psql
db-psql: ## open a psql shell
	$(COMPOSE) exec postgres psql -U paas -d paas

.PHONY: migrate-up
migrate-up: ## apply all pending migrations
	$(GOOSE) -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## roll back the last migration
	$(GOOSE) -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" down

.PHONY: migrate-status
migrate-status: ## show migration status
	$(GOOSE) -dir $(MIGRATIONS) postgres "$(DATABASE_URL)" status

.PHONY: migrate-new
migrate-new: ## create a migration: make migrate-new name=add_something
	$(GOOSE) -dir $(MIGRATIONS) create $(name) sql

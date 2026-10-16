.PHONY: service test vet jwt-key up down down-v logs ps deps migration tools proto proto-lint proto-format proto-check proto-breaking

BIN := $(CURDIR)/.bin
export PATH := $(BIN):$(PATH)

BUF_VERSION          := v1.73.0
PROTOC_GEN_GO_VERSION := v1.36.12
CONNECT_GO_VERSION   := v1.21.0

# make service name=billing
service:
	@test -n "$(name)" || (echo "usage: make service name=<name>" && exit 1)
	@./scripts/new-service.sh $(name)

# ---- local stack (deploy/compose.yaml) ----
COMPOSE := docker compose -f deploy/compose.yaml

up:
	$(COMPOSE) up -d --build
	@echo "gateway http://localhost:8080 | grafana http://localhost:3030 | alloy http://localhost:12345"

# only Postgres + Redis, to run services with `go run` against them
deps:
	$(COMPOSE) up -d --wait postgres redis

down:
	$(COMPOSE) down

down-v:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f --tail=100 iam note gateway

ps:
	$(COMPOSE) ps

# new goose migration: make migration svc=iam name=add_avatar   (svc=template for the template)
migration:
	@test -n "$(svc)" -a -n "$(name)" || (echo "usage: make migration svc=<service> name=<snake_case>" && exit 1)
	@dir=$$( [ "$(svc)" = template ] && echo templates/service || echo services/$(svc) ); \
	test -d "$$dir/migrations" || (echo "$$dir/migrations not found" && exit 1); \
	f="$$dir/migrations/$$(date -u +%Y%m%d%H%M%S)_$(name).sql"; \
	printf -- '-- +goose Up\n\n-- +goose Down\n' > "$$f" && echo "created $$f"

# Ed25519 signing key for iam access tokens: make jwt-key out=services/iam/jwt.pem
out ?= services/iam/jwt.pem
jwt-key:
	@test ! -e "$(out)" || (echo "$(out) already exists, refusing to overwrite" && exit 1)
	openssl genpkey -algorithm ed25519 -out "$(out)"
	chmod 600 "$(out)"
	@echo "set JWT_PRIVATE_KEY_FILE=$(out) for iam"

# every module in go.work, by import path (./... patterns skip dirs starting with "_")
MODULES = $(shell go list -m | sed 's|$$|/...|')

test:
	go test $(MODULES)

vet:
	go vet $(MODULES)

# ---- contracts (api/) ----

# installs pinned codegen tools into .bin/ (nothing global)
tools:
	GOBIN=$(BIN) go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	GOBIN=$(BIN) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	GOBIN=$(BIN) go install connectrpc.com/connect/cmd/protoc-gen-connect-go@$(CONNECT_GO_VERSION)

$(BIN)/buf $(BIN)/protoc-gen-go $(BIN)/protoc-gen-connect-go:
	@$(MAKE) tools

proto: $(BIN)/buf $(BIN)/protoc-gen-go $(BIN)/protoc-gen-connect-go
	cd api && buf format -w && buf lint && buf generate

proto-lint: $(BIN)/buf
	cd api && buf format -d --exit-code && buf lint

# fails if api/gen is out of date with api/proto (use in CI)
proto-check: $(BIN)/buf $(BIN)/protoc-gen-go $(BIN)/protoc-gen-connect-go
	@tmp=$$(mktemp -d) && trap 'rm -rf $$tmp' EXIT && \
	cd api && buf generate -o $$tmp && \
	diff -r $$tmp/gen gen && echo "api/gen is up to date"

# make proto-breaking against='.git#branch=main'
against ?= .git\#branch=main,subdir=api
proto-breaking: $(BIN)/buf
	cd api && buf breaking --against '../$(against)'

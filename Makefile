.PHONY: service test vet jwt-key jwt-key-activate up down down-v logs ps deps migration admin-web admin-web-dev tools proto proto-lint proto-format proto-check proto-breaking

BIN := $(CURDIR)/.bin
export PATH := $(BIN):$(PATH)

BUF_VERSION          := v1.73.0
PROTOC_GEN_GO_VERSION := v1.36.12
CONNECT_GO_VERSION   := v1.21.0

# make service app=notes name=billing  → apps/notes/services/billing
service:
	@test -n "$(app)" -a -n "$(name)" || (echo "usage: make service app=<app> name=<name>" && exit 1)
	@./scripts/new-service.sh $(app) $(name)

# ---- local stack (deploy/compose.yaml) ----
COMPOSE := docker compose -f deploy/compose.yaml

up:
	$(COMPOSE) up -d --build
	@echo "admin http://localhost:8090 | notes http://localhost:8081 | grafana http://localhost:3030 | alloy http://localhost:12345 | mail http://localhost:8025"

# only Postgres, Redis and Mailpit, to run services with `go run` against them
deps:
	$(COMPOSE) up -d --wait postgres redis mailpit

down:
	$(COMPOSE) down

down-v:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f --tail=100 admin notes-gateway note

ps:
	$(COMPOSE) ps

# new goose migration: make migration svc=iam name=add_avatar   (svc=template for the template)
migration:
	@test -n "$(svc)" -a -n "$(name)" || (echo "usage: make migration svc=<service> name=<snake_case>" && exit 1)
	@dir=$$( case "$(svc)" in template) echo templates/service;; \
		*) ls -d platform/$(svc) apps/*/services/$(svc) 2>/dev/null | head -1;; esac ); \
	test -n "$$dir" || (echo "no service named $(svc) in platform/ or apps/*/services/" && exit 1); \
	test -d "$$dir/migrations" || (echo "$$dir/migrations not found" && exit 1); \
	f="$$dir/migrations/$$(date -u +%Y%m%d%H%M%S)_$(name).sql"; \
	printf -- '-- +goose Up\n\n-- +goose Down\n' > "$$f" && echo "created $$f"

# admin dashboard (admin-dashboard/): build into platform/admin/web/dist (embedded
# by the admin service), or run the dev server on :5173 against the admin API on :8090
admin-web:
	cd admin-dashboard && npm ci --no-audit --no-fund && npm run build

admin-web-dev:
	cd admin-dashboard && npm install --no-audit --no-fund && npm run dev

# Ed25519 signing keys for an app's access tokens (docs/runbooks/jwt-key-rotation.md):
#   make jwt-key out=apps/notes/gateway/jwt.pem   a single key (JWT_PRIVATE_KEY_FILE)
#   make jwt-key keys=<dir> name=2026-11-01       add a key to a key directory (JWT_KEYS_DIR); the
#                                                 current key keeps signing until jwt-key-activate
#   make jwt-key-activate keys=<dir> name=2026-11-01
out ?= apps/notes/gateway/jwt.pem
jwt-key:
	@if [ -n "$(keys)" ]; then \
		[ -n "$(name)" ] || { echo "usage: make jwt-key keys=<dir> name=<name, e.g. 2026-11-01>"; exit 1; }; \
		f="$(keys)/$(name).pem"; \
		[ ! -e "$$f" ] || { echo "$$f already exists, refusing to overwrite"; exit 1; }; \
		mkdir -p "$(keys)"; \
		cur=$$(ls "$(keys)" | grep '\.pem$$' | sort | tail -n 1); \
		if [ -n "$$cur" ] && [ ! -e "$(keys)/active" ]; then \
			echo "$${cur%.pem}" > "$(keys)/active"; \
			echo "pinned the current key: $(keys)/active = $${cur%.pem}"; \
		fi; \
		openssl genpkey -algorithm ed25519 -out "$$f" && chmod 600 "$$f" || exit 1; \
		if [ -z "$$cur" ]; then \
			echo "added $$f, the first key: it signs. Set JWT_KEYS_DIR=$(keys)"; \
		else \
			echo "added $$f (verify only). Once every verifier has it (>= 10 min), run:"; \
			echo "  make jwt-key-activate keys=$(keys) name=$(name)"; \
		fi; \
	else \
		[ ! -e "$(out)" ] || { echo "$(out) already exists, refusing to overwrite"; exit 1; }; \
		openssl genpkey -algorithm ed25519 -out "$(out)" && chmod 600 "$(out)" || exit 1; \
		echo "set JWT_PRIVATE_KEY_FILE=$(out) for the service that signs the tokens"; \
	fi

jwt-key-activate:
	@[ -n "$(keys)" ] && [ -n "$(name)" ] || { echo "usage: make jwt-key-activate keys=<dir> name=<key name>"; exit 1; }
	@[ -e "$(keys)/$(name).pem" ] || { echo "$(keys)/$(name).pem not found"; exit 1; }
	@echo "$(name)" > "$(keys)/active"
	@echo "$(name) signs from the next reload (JWT_KEYS_RELOAD, default 60s)."
	@echo "Remove the previous key after the access token lifetime + 30s (default 630s)."

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

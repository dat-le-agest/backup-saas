#!/usr/bin/env bash
# Create a new service in services/ from templates/service.
#
# Usage: scripts/new-service.sh <name>
#   name: lowercase letters, digits and hyphens, starting with a letter (e.g. billing, user-profile)
#
# What it does:
#   1. copies templates/service/ to services/<name>/ (skipping .env, *.db and build artifacts)
#   2. renames the Go module and every "template/..." import to "<name>/..."
#   3. sets config.ServiceName and the default database names
#   4. registers the module in go.work
#   5. builds and tests the new service

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE_DIR="$ROOT/templates/service"
TEMPLATE_MODULE="template"
# Go module names already used in the workspace
RESERVED=(api core "$TEMPLATE_MODULE")

die() {
	echo "error: $*" >&2
	exit 1
}

usage() {
	echo "Usage: $(basename "$0") <name>" >&2
	exit 1
}

[[ $# -eq 1 ]] || usage
NAME="$1"

[[ "$NAME" =~ ^[a-z][a-z0-9-]*$ ]] || die "invalid name '$NAME' (allowed: lowercase letters, digits, hyphens; must start with a letter)"
[[ "$NAME" != *- ]] || die "name must not end with a hyphen"

for r in "${RESERVED[@]}"; do
	[[ "$NAME" != "$r" ]] || die "'$NAME' is reserved"
done

TARGET_DIR="$ROOT/services/$NAME"
[[ -d "$TEMPLATE_DIR" ]] || die "template not found: $TEMPLATE_DIR"
[[ ! -e "$TARGET_DIR" ]] || die "'$NAME' already exists: $TARGET_DIR"
command -v go >/dev/null || die "go is not installed"

# database names can't contain hyphens without quoting
DB_NAME="${NAME//-/_}"

cleanup_on_error() {
	echo "failed, removing $TARGET_DIR" >&2
	rm -rf "$TARGET_DIR"
	# undo go.work registration if it already happened
	(cd "$ROOT" && go work edit -dropuse="./services/$NAME" 2>/dev/null) || true
}
trap cleanup_on_error ERR

echo "→ copying templates/service to services/$NAME/"
mkdir -p "$TARGET_DIR"
tar -C "$TEMPLATE_DIR" \
	--exclude='.env' \
	--exclude='*.db' \
	--exclude='*.db-journal' \
	-cf - . | tar -C "$TARGET_DIR" -xf -
# binary produced by `go build ./cmd/server` inside the template
if [[ -f "$TARGET_DIR/server" ]]; then rm -f "$TARGET_DIR/server"; fi

echo "→ renaming module $TEMPLATE_MODULE → $NAME"
# sed -i.bak works on both GNU and BSD sed
replace() {
	local expr="$1"
	shift
	sed -i.bak -e "$expr" "$@"
	for f in "$@"; do rm -f "$f.bak"; done
}

replace "s#^module $TEMPLATE_MODULE\$#module $NAME#" "$TARGET_DIR/go.mod"

GO_FILES=()
while IFS= read -r -d '' f; do GO_FILES+=("$f"); done < <(find "$TARGET_DIR" -name '*.go' -print0)
replace "s#\"$TEMPLATE_MODULE/#\"$NAME/#g" "${GO_FILES[@]}"

replace "s#^const ServiceName = \"$TEMPLATE_MODULE\"\$#const ServiceName = \"$NAME\"#" "$TARGET_DIR/config/config.go"

if [[ -f "$TARGET_DIR/.env.example" ]]; then
	replace "s#\./$TEMPLATE_MODULE\.db#./$DB_NAME.db#; s#)/$TEMPLATE_MODULE?#)/$DB_NAME?#" "$TARGET_DIR/.env.example"
fi

# sanity check: nothing should still point at the template module
if grep -rqE "\"$TEMPLATE_MODULE/|^module $TEMPLATE_MODULE\$" "$TARGET_DIR"; then
	grep -rnE "\"$TEMPLATE_MODULE/|^module $TEMPLATE_MODULE\$" "$TARGET_DIR" >&2
	die "leftover references to '$TEMPLATE_MODULE'"
fi

# renamed imports may no longer be sorted within their group
gofmt -w "$TARGET_DIR"

echo "→ registering ./services/$NAME in go.work"
(cd "$ROOT" && go work use "./services/$NAME")

echo "→ building and testing"
(cd "$TARGET_DIR" && go vet ./... && go test ./...)

trap - ERR

cat <<EOF

✓ service '$NAME' created at services/$NAME/

Next steps:
  1. give it a database: add a role + database to deploy/postgres/init.sql
     (or create them by hand) and set DB_DSN in services/$NAME/.env.example
  2. cd services/$NAME && cp .env.example .env && go run ./cmd/server
  3. to run it in the stack: add it to deploy/compose.yaml (SERVICE: services/$NAME)

The 'note' module is an example: rename or remove it in
  internal/{model,service,repository,transport} and cmd/server/{container.go,main.go}
EOF

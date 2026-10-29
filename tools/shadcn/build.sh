#!/usr/bin/env bash
# Regenerates the committed shadcn-templ assets:
#   internal/web/static/shadcn-templ-<hash>.js + internal/web/ui/scripts_bundle.go
#   internal/web/static/shadcn.css
#
# Dev only. Runtime and self-hosting stay `go build`: both outputs are committed
# and go:embed-ed by internal/web/templates.go. Run with `mise run ui:gen`.
set -euo pipefail

SHADCN_VERSION="v2.0.0-beta.16"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if [ ! -x tools/shadcn/node_modules/.bin/tailwindcss ]; then
  echo "installing dev-only Tailwind packages"
  (cd tools/shadcn && npm ci --no-audit --no-fund --silent)
fi

echo "bundling shadcn-templ component JS @ $SHADCN_VERSION"
go run "github.com/axadrn/shadcn-templ/v2/cmd/shadcn-templ@$SHADCN_VERSION" bundle

echo "building Tailwind CSS"
tools/shadcn/node_modules/.bin/tailwindcss \
  -i tools/shadcn/tailwind.css \
  -o internal/web/static/shadcn.css \
  --minify

#!/bin/bash
set -euo pipefail

# Only run in remote (web) sessions
if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

cd "${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel)}"

# Pre-download all Go module dependencies (including CGO sqlite3 bindings)
echo "Downloading Go modules..."
go mod download

echo "Session start hook complete."

#!/bin/sh
# Runs the browser tests against a throwaway instance of the current
# code (sample mail served from memory). Python, Playwright and Chromium come
# from nixpkgs. Screenshots go to tests/e2e/out.
set -eu
cd "$(dirname "$0")/../.."
mkdir -p tests/e2e/out
tmp=$(mktemp -d)
trap 'kill $pid 2>/dev/null || true; rm -rf "$tmp"' EXIT

go build -tags e2e -o "$tmp/mail" ./cmd/server
"$tmp/mail" -port 18090 -data-dir "$tmp/data" >"$tmp/server.log" 2>&1 &
pid=$!
sleep 1

python=$(nix build --no-link --print-out-paths --impure --expr \
  'let p = import (builtins.getFlake "nixpkgs") {}; in p.python3.withPackages (ps: [ ps.playwright ])')
browsers=$(nix build --no-link --print-out-paths --impure --expr \
  'let p = import (builtins.getFlake "nixpkgs") {}; in p.playwright-driver.browsers')

PLAYWRIGHT_BROWSERS_PATH="$browsers" BASE_URL=http://localhost:18090 OUT=tests/e2e/out \
  "$python/bin/python3" -I tests/e2e/smoke.py || { cat "$tmp/server.log"; exit 1; }

#!/usr/bin/env bash
# Build the MultiversX chain simulator and the validator-key generator from source.
# Idempotent: skips a build if the binary already exists (pass --force to rebuild).

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

FORCE="${1:-}"
need go
ensure_sim_src

mkdir -p "$BUILD"

if [ "$FORCE" = "--force" ] || [ ! -x "$SIM_BIN" ]; then
  log "building chain simulator (pulls the VM / cgo deps; first build is slow)…"
  ( cd "$SIM_SRC" && go build -o "$SIM_BIN" ./cmd/chainsimulator )
  ok "built ${SIM_BIN#$ROOT/}"
else
  ok "chain simulator already built (${SIM_BIN#$ROOT/}) — use --force to rebuild"
fi

if [ "$FORCE" = "--force" ] || [ ! -x "$KEYGEN_BIN" ]; then
  log "building validator keygenerator (from the mx-chain-go dependency)…"
  ( cd "$SIM_SRC" && go build -o "$KEYGEN_BIN" github.com/multiversx/mx-chain-go/cmd/keygenerator )
  ok "built ${KEYGEN_BIN#$ROOT/}"
else
  ok "keygenerator already built (${KEYGEN_BIN#$ROOT/}) — use --force to rebuild"
fi

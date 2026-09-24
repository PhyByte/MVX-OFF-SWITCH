#!/usr/bin/env bash
# Prove gossip PROPAGATION: one node that does NOT act on the trigger broadcasts a single
# authority-signed hardfork message onto a real libp2p network, and every other node that
# trusts the key halts.
#
# Injects p2p-poc/propagation/main.go into the simulator module (to inherit pinned deps),
# builds it, and runs it. It stands up real libp2p messengers on 127.0.0.1 (1 attacker with
# no trigger + N victims running the real interceptor + trigger), broadcasts once, and shows
# all victims halt while the attacker is unaffected.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need go
ensure_sim_src

SRC="$ROOT/p2p-poc/propagation/main.go"
[ -f "$SRC" ] || die "missing $SRC"
DEST_DIR="$SIM_SRC/cmd/p2ppropagation"

log "injecting the propagation proof into the simulator module…"
mkdir -p "$DEST_DIR"
cp "$SRC" "$DEST_DIR/main.go"

log "building the propagation harness…"
# This harness imports the communication-go libp2p test helpers, which pull a few extra
# modules not in the upstream go.mod — resolve them (safe: only adds missing requires).
( cd "$SIM_SRC" && go mod tidy >/dev/null 2>&1 && go build -o "$P2P_PROP_BIN" ./cmd/p2ppropagation )
ok "built ${P2P_PROP_BIN#$ROOT/}"

log "running (real libp2p on 127.0.0.1; halts observed via each node's closer)…"
echo >&2
"$P2P_PROP_BIN"

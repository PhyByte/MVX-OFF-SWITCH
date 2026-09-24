#!/usr/bin/env bash
# Prove the offswitch over the P2P path (no REST API), using unmodified mx-chain-go code.
#
# Injects p2p-poc/main.go into the simulator module (so it inherits the exact pinned
# dependency versions + replace directives), builds it, and runs it. The program has an
# "attacker" holding only the authority BLS private key (not a validator) craft + sign a
# peerAuthentication hardfork message, feeds it through the REAL interceptor + trigger, and
# shows the node emit its HardForkExport stop signal. Non-authority keys and forged
# signatures are rejected.
#
# NOTE: it waits ~2 minutes for the stop signal (CloseAfterExportInMinutes minimum is 2).

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need go
ensure_sim_src

SRC="$ROOT/p2p-poc/proof/main.go"
[ -f "$SRC" ] || die "missing $SRC"
DEST_DIR="$SIM_SRC/cmd/p2poffswitch"

log "injecting the P2P proof into the simulator module…"
mkdir -p "$DEST_DIR"
cp "$SRC" "$DEST_DIR/main.go"

log "building the proof harness…"
( cd "$SIM_SRC" && go build -o "$P2P_BIN" ./cmd/p2poffswitch )
ok "built ${P2P_BIN#$ROOT/}"

log "running (waits ~2 min for the stop signal)…"
echo >&2
"$P2P_BIN"

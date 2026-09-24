#!/usr/bin/env bash
# =============================================================================
#  test_p2p — the P2P gossip path (the real, network-wide offswitch)
#
#  Uses UNMODIFIED mx-chain-go code (real interceptor + real hardfork trigger) to show
#  that a peerAuthentication message signed by the authority BLS private key — sent by a
#  non-validator, over the gossip path, with no REST API — is accepted and halts the node.
#  Controls show a non-authority key and a forged signature are rejected.
#
#  This is the vector that api.toml (Open = false) does NOT close: it is governed by
#  config.toml EnableTriggerFromP2P, which is true by default on mainnet.
#
#  Usage:  scripts/test_p2p.sh
#  NOTE:   waits ~2 min for the stop signal (CloseAfterExportInMinutes minimum is 2).
# =============================================================================
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

banner "MultiversX offswitch — P2P gossip path"
info "No REST API, no libp2p host, sender is NOT a validator and does NOT run the node."

phase "1/1" "Build + run the P2P proof against real node code"
bash "$HERE/04_p2p_proof.sh"

banner "test_p2p done"

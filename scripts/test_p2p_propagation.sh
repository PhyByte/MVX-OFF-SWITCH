#!/usr/bin/env bash
# =============================================================================
#  test_p2p_propagation — one broadcaster halts the whole network over gossip
#
#  Stands up a REAL libp2p network on 127.0.0.1:
#    * 1 "attacker" node that holds the authority BLS private key but has NO hardfork
#      trigger of its own — it only broadcasts.
#    * N "victim" nodes running UNMODIFIED mx-chain-go reception code (real interceptor +
#      real hardfork trigger), each configured with PublicKeyToListenFrom = authority key.
#
#  The attacker broadcasts ONE signed hardfork message. libp2p gossip delivers it; every
#  victim accepts it and halts. The attacker (no trigger) is unaffected.
#
#  This is the network-wide fan-out that a single key-holder gets for free from gossip —
#  the vector api.toml (REST disabled) does not touch.
# =============================================================================
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

banner "MultiversX offswitch — P2P propagation (one broadcaster → whole network)"
info "Real libp2p transport; sender has NO trigger; victims run unmodified node code."

phase "1/1" "Build + run the propagation proof"
bash "$HERE/05_p2p_propagation.sh"

banner "test_p2p_propagation done"

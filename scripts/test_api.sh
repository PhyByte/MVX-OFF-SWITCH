#!/usr/bin/env bash
# =============================================================================
#  test_api — the REST / API path
#
#  Boots a local MultiversX chain simulator, installs OUR generated BLS key as the
#  hardfork authority (Hardfork.PublicKeyToListenFrom), then halts a node by calling
#  its POST /hardfork/trigger endpoint.
#
#  Usage:  scripts/test_api.sh
#          KEEP_RUNNING=1 scripts/test_api.sh   # leave the simulator running
# =============================================================================
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

banner "MultiversX offswitch — REST / API path"
info "Replicates mainnet: the /hardfork/trigger route is DISABLED (api.toml Open=false),"
info "so the local REST vector returns 404. Run with REST_OPEN=1 to instead see the"
info "trigger actually fire. Either way, the network-wide vector is test_p2p.sh."

phase "1/4" "Build simulator + keygenerator"
bash "$HERE/00_build.sh"

phase "2/4" "Generate a fresh BLS key"
bash "$HERE/01_genkey.sh"

phase "3/4" "Install our key into the node config"
bash "$HERE/02_setup_config.sh"

phase "4/4" "Run the chain and fire the REST trigger"
bash "$HERE/03_run_and_trigger.sh"

banner "test_api done"

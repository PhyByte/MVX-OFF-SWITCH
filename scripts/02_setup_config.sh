#!/usr/bin/env bash
# Prepare a self-contained run directory and install OUR generated key as the hardfork
# trigger authority in the node config file.
#
#   1. Copy the simulator's own config skeleton into work/run/config.
#   2. Fetch node + proxy configs from the pinned versions (once; skipped if present).
#   3. Edit work/run/config/node/config/config.toml:
#        [Hardfork] PublicKeyToListenFrom     -> our generated pubkey  ("replace the key")
#        [Hardfork] CloseAfterExportInMinutes -> 2  (minimum allowed; 0 is rejected)

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

need curl
[ -x "$SIM_BIN" ] || die "simulator missing — run scripts/00_build.sh first"
[ -f "$KEYS/pubkey.hex" ] || die "no generated key — run scripts/01_genkey.sh first"
PUB="$(cat "$KEYS/pubkey.hex")"

RUN="$WORK/run"
NODE_CFG="$RUN/config/node/config/config.toml"

log "setting up run directory (${RUN#$ROOT/})…"
mkdir -p "$RUN/config"
cp -R "$SIM_SRC/cmd/chainsimulator/config/." "$RUN/config/"

if [ ! -f "$NODE_CFG" ]; then
  log "fetching node + proxy configs (matched to the simulator build)…"
  ( cd "$RUN" && "$SIM_BIN" --fetch-configs-and-close >/dev/null 2>&1 )
  [ -f "$NODE_CFG" ] || die "config fetch failed — no $NODE_CFG"
  ok "configs fetched"
else
  ok "node configs already present"
fi

ORIG="$(grep -E '^[[:space:]]*PublicKeyToListenFrom' "$NODE_CFG" | head -1 | sed -E 's/.*"([0-9a-f]*)".*/\1/')"
printf '%s' "$ORIG" > "$KEYS/original_pubkey.hex"

log "installing our key as the offswitch (Hardfork.PublicKeyToListenFrom)…"
# Portable in-place edit (works with both BSD/macOS and GNU/Linux sed).
sed -E "s|^([[:space:]]*PublicKeyToListenFrom[[:space:]]*=[[:space:]]*).*|\1\"${PUB}\"|" "$NODE_CFG" > "$NODE_CFG.tmp" && mv "$NODE_CFG.tmp" "$NODE_CFG"
sed -E "s|^([[:space:]]*CloseAfterExportInMinutes[[:space:]]*=[[:space:]]*).*|\12|" "$NODE_CFG" > "$NODE_CFG.tmp" && mv "$NODE_CFG.tmp" "$NODE_CFG"

ok "config patched"
kv "config file" "${NODE_CFG#$ROOT/}"
kv "was (shipped key)" "${ORIG:0:32}…"
kv "now (our key)" "${PUB:0:32}…"

# Mirror mainnet: disable the REST /hardfork/trigger route (api.toml Open=false).
# The node repo ships it Open=true; mainnet-config overrides it to false. Set REST_OPEN=1
# to keep it open (to demonstrate the REST trigger actually firing).
API_CFG="$RUN/config/node/config/api.toml"
if [ -f "$API_CFG" ]; then
  if [ "${REST_OPEN:-0}" = "1" ]; then
    sed -E 's|(\{ Name = "/trigger", Open = )false|\1true|' "$API_CFG" > "$API_CFG.tmp" && mv "$API_CFG.tmp" "$API_CFG"
    warn "REST /hardfork/trigger left OPEN (REST_OPEN=1) — NOT mainnet-accurate"
  else
    sed -E 's|(\{ Name = "/trigger", Open = )true|\1false|' "$API_CFG" > "$API_CFG.tmp" && mv "$API_CFG.tmp" "$API_CFG"
    ok "REST /hardfork/trigger DISABLED (Open=false) — matches mainnet"
  fi
  kv "api.toml" "${API_CFG#$ROOT/}"
  kv "/trigger route" "$(grep '"/trigger"' "$API_CFG")"
fi

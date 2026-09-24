#!/usr/bin/env bash
# Boot the local chain, advance past epoch 1, then fire the hardfork "offswitch" by
# POSTing to a node's /hardfork/trigger endpoint (the REST path). Observe the result.
#
# See README.md for what this proves and its limits. In short: our generated key is
# accepted as PublicKeyToListenFrom, and POST /hardfork/trigger drives the node into the
# hardfork EXPORT/shutdown sequence. The single-node simulator keeps producing blocks via
# its manual driver and never consumes chanStopNodeProcess, so it does not self-terminate.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
need curl; need python3

RUN="$WORK/run"
NODE_CFG="$RUN/config/node/config/config.toml"
[ -f "$NODE_CFG" ] || die "config not set up — run scripts/02_setup_config.sh first"

KEEP="${KEEP_RUNNING:-0}"   # set KEEP_RUNNING=1 to leave the sim up after the run

cleanup() {
  if [ "$KEEP" != "1" ] && [ -f "$PID_FILE" ]; then
    kill "$(cat "$PID_FILE")" 2>/dev/null || true
    sleep 1; kill -9 "$(cat "$PID_FILE")" 2>/dev/null || true
    rm -f "$PID_FILE"
    log "simulator stopped"
  fi
}
trap cleanup EXIT

# --- start the simulator -----------------------------------------------------
log "starting the chain simulator (node logs -> ${SIM_LOG#$ROOT/})…"
rm -f "$SIM_LOG"
( cd "$RUN" && exec "$SIM_BIN" --skip-configs-download --log-level "*:INFO" ) >"$SIM_LOG" 2>&1 &
echo $! > "$PID_FILE"
log "simulator pid $(cat "$PID_FILE")"

# --- wait for the proxy ------------------------------------------------------
log "waiting for the proxy on $PROXY_URL …"
for i in $(seq 1 60); do
  if [ -n "$(curl -s -m 3 "$PROXY_URL/simulator/observers" 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin).get("data") or "")' 2>/dev/null)" ]; then
    ok "proxy is up"
    break
  fi
  kill -0 "$(cat "$PID_FILE")" 2>/dev/null || { tail -20 "$SIM_LOG" >&2; die "simulator exited during startup"; }
  sleep 1
  [ "$i" = 60 ] && die "proxy did not come up in time"
done

# --- discover the metachain node's REST API port -----------------------------
OBS_JSON="$(curl -s -m 5 "$PROXY_URL/simulator/observers")"
META_PORT="$(echo "$OBS_JSON" | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["4294967295"]["api-port"])')"
[ -n "$META_PORT" ] || die "could not find metachain node api port"
ok "chain is live"
kv "shard API ports" "$(echo "$OBS_JSON" | python3 -c 'import sys,json;d=json.load(sys.stdin)["data"];print(", ".join("%s:%s"%(k,v["api-port"]) for k,v in d.items()))')"
kv "metachain node API" "port $META_PORT"

# --- advance past epoch 1 (rounds-per-epoch = 20) ----------------------------
log "generating 25 blocks to reach epoch >= 1 …"
curl -s -m 90 -X POST "$PROXY_URL/simulator/generate-blocks/25" >/dev/null
read -r EPOCH NONCE_BEFORE <<EOF
$(curl -s -m 5 "$PROXY_URL/network/status/4294967295" | python3 -c 'import sys,json;s=json.load(sys.stdin)["data"]["status"];print(s["erd_epoch_number"],s["erd_nonce"])')
EOF
kv "meta epoch / nonce" "$EPOCH / $NONCE_BEFORE"
[ "$EPOCH" -ge 1 ] || die "epoch still 0; cannot trigger (minimum epoch is 1)"

# --- FIRE THE OFFSWITCH ------------------------------------------------------
phase "REST" "firing POST http://localhost:$META_PORT/hardfork/trigger"
RESP="$(curl -s -m 10 -X POST "http://localhost:$META_PORT/hardfork/trigger" \
        -H 'Content-Type: application/json' \
        -d "{\"epoch\":$EPOCH,\"withEarlyEndOfEpoch\":false}")"
kv "response" "$RESP"
sleep 4

# --- observe -----------------------------------------------------------------
log "node log (trigger sequence):"
grep -iE "hardfork trigger|started hardFork export|hardFork export process" "$SIM_LOG" | tail -4 \
  | sed -E 's/\x1b\[[0-9;]*m//g; s/^/      /' >&2

TRIGGERED=$(grep -c "hardfork trigger" "$SIM_LOG" || true)
EXPORTING=$(grep -c "started hardFork export process" "$SIM_LOG" || true)

NONCE_AFTER="$(curl -s -m 5 "$PROXY_URL/network/status/4294967295" 2>/dev/null | python3 -c 'import sys,json;print(json.load(sys.stdin)["data"]["status"]["erd_nonce"])' 2>/dev/null || echo '?')"

echo >&2
if [ "$TRIGGERED" -ge 1 ] && [ "$EXPORTING" -ge 1 ]; then
  pass "trigger ACCEPTED with our key; node entered the hardfork EXPORT/shutdown sequence"
else
  warn "trigger did not visibly engage — inspect ${SIM_LOG#$ROOT/}"
fi
case "$RESP" in
  *"broadcast to other peers"*) kv "scope" "IsSelfTrigger=TRUE  → would BROADCAST a network-wide halt";;
  *"only the current node"*)    kv "scope" "IsSelfTrigger=FALSE → local only (sim node self-key != our key; expected)";;
esac
kv "meta nonce after" "$NONCE_AFTER (sim keeps producing via its manual driver — see README 'Limitations')"

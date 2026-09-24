#!/usr/bin/env bash
# Generate a fresh BLS validator keypair. This is the key we install as the hardfork
# "offswitch" authority (config field Hardfork.PublicKeyToListenFrom).
#
# The keygenerator writes a PEM whose header carries the hex public key:
#   -----BEGIN PRIVATE KEY for <192-hex-char BLS pubkey>-----
# We extract that pubkey into work/keys/pubkey.hex.

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

[ -x "$KEYGEN_BIN" ] || die "keygenerator missing — run scripts/00_build.sh first"

mkdir -p "$KEYS"
rm -f "$KEYS/validatorKey.pem" "$KEYS/pubkey.hex"

log "generating a BLS validator key…"
( cd "$KEYS" && "$KEYGEN_BIN" --key-type validator --num-keys 1 >/dev/null 2>&1 )
[ -f "$KEYS/validatorKey.pem" ] || die "keygen did not produce validatorKey.pem"

PUB="$(grep -o 'for [0-9a-f]*' "$KEYS/validatorKey.pem" | head -1 | awk '{print $2}')"
[ "${#PUB}" -eq 192 ] || die "unexpected pubkey length ${#PUB} (want 192 hex chars for a BLS key)"
printf '%s' "$PUB" > "$KEYS/pubkey.hex"

ok "generated a fresh BLS validator key"
kv "private key file" "${KEYS#$ROOT/}/validatorKey.pem"
kv "public key (hex)" "${PUB:0:32}…  (${#PUB} chars)"

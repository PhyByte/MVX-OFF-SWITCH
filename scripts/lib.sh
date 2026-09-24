# shellcheck shell=bash
# Shared paths and helpers for the MultiversX "offswitch" (hardfork trigger) PoC.
# Sourced by the other scripts. Not meant to be run directly.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export ROOT
export SIM_SRC="$ROOT/mx-chain-simulator-go"
export BUILD="$ROOT/build"
export WORK="$ROOT/work"
export KEYS="$WORK/keys"
export NODE_CONFIG="$WORK/node-config"     # legacy alias (unused)
export SIM_BIN="$BUILD/chainsimulator"
export KEYGEN_BIN="$BUILD/keygenerator"
export P2P_BIN="$BUILD/p2poffswitch"
export P2P_PROP_BIN="$BUILD/p2ppropagation"
export SIM_LOG="$WORK/sim.log"
export PID_FILE="$WORK/sim.pid"

# Proxy port the chain simulator exposes (from config.toml [config.simulator] server-port).
export PROXY_PORT="${PROXY_PORT:-8085}"
export PROXY_URL="http://localhost:${PROXY_PORT}"

# ---- colors (disabled when stdout is not a terminal) ----
if [ -t 2 ]; then
  C_RESET=$'\033[0m'; C_DIM=$'\033[2m'; C_BOLD=$'\033[1m'
  C_BLUE=$'\033[1;34m'; C_GREEN=$'\033[1;32m'; C_YELLOW=$'\033[1;33m'
  C_RED=$'\033[1;31m'; C_CYAN=$'\033[1;36m'; C_MAGENTA=$'\033[1;35m'
else
  C_RESET=; C_DIM=; C_BOLD=; C_BLUE=; C_GREEN=; C_YELLOW=; C_RED=; C_CYAN=; C_MAGENTA=
fi

_ts() { date +%H:%M:%S; }

# banner "TITLE" — a boxed section header
banner() {
  local msg="$1" width=64 line
  line="$(printf '%*s' "$width" '' | tr ' ' '=')"
  printf '\n%s%s%s\n'   "$C_CYAN" "$line" "$C_RESET" >&2
  printf '%s  %s%s\n'    "$C_CYAN$C_BOLD" "$msg" "$C_RESET" >&2
  printf '%s%s%s\n'      "$C_CYAN" "$line" "$C_RESET" >&2
}

# phase "N/T" "description"
phase() { printf '\n%s▶ [%s] %s%s\n' "$C_MAGENTA$C_BOLD" "$1" "$2" "$C_RESET" >&2; }

log()  { printf '%s%s ·%s %s\n'  "$C_DIM" "$(_ts)" "$C_RESET" "$*" >&2; }
info() { printf '%s[i]%s %s\n'   "$C_BLUE"   "$C_RESET" "$*" >&2; }
ok()   { printf '%s[+]%s %s\n'   "$C_GREEN"  "$C_RESET" "$*" >&2; }
warn() { printf '%s[!]%s %s\n'   "$C_YELLOW" "$C_RESET" "$*" >&2; }
fail() { printf '%s[x]%s %s\n'   "$C_RED"    "$C_RESET" "$*" >&2; }
pass() { printf '%s[✓ PASS]%s %s\n' "$C_GREEN$C_BOLD" "$C_RESET" "$*" >&2; }

# kv "label" "value" — aligned key/value line
kv()   { printf '      %s%-26s%s %s\n' "$C_DIM" "$1" "$C_RESET" "$2" >&2; }

die()  { fail "$*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

# ensure the (non-vendored) upstream simulator source is present
ensure_sim_src() {
  need git
  if [ ! -d "$SIM_SRC" ]; then
    info "cloning mx-chain-simulator-go (not vendored in this repo)…"
    git clone --depth 1 https://github.com/multiversx/mx-chain-simulator-go.git "$SIM_SRC" 2>&1 \
      | sed 's/^/      /' >&2
    ok "cloned into ${SIM_SRC#$ROOT/}"
  fi
}

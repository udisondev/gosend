package p2pclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/udisondev/gosend/internal/client"
	"github.com/udisondev/gosend/pkg/protocol"
)

type SignalMessage struct {
	Type      string                     `json:"type"`
	SDP       *webrtc.SessionDescription `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
}

type Peer struct {
	HexID  string
	inCh   <-chan string
	sendCh chan string
	close  func()
}

func (p *Peer) Send(msg string) error {
	select {
	case p.sendCh <- msg:
		return nil
	default:
		return errors.New("send buffer full")
	}
}

func (p *Peer) Inbox() <-chan string {
	return p.inCh
}

func (p *Peer) Close() {
	p.close()
}

type peerState struct {
	pc        *webrtc.PeerConnection
	dc        *webrtc.DataChannel
	inCh      chan string
	sendCh    chan string
	connected chan struct{}
	close     func()
}

type P2PClient struct {
	baseClient *client.Client
	myID       [protocol.ClientIDSize]byte
	myHexID    string
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	peers      map[string]*peerState
	newPeers   chan *Peer
	logger     *slog.Logger
}

func NewP2PClient(parentCtx context.Context, addr string) (*P2PClient, <-chan *Peer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate keys: %w", err)
	}
	myID := [protocol.ClientIDSize]byte(pub)
	myHexID := hex.EncodeToString(pub)
	slog.Debug("Generated public key", "hex", myHexID)
	fmt.Printf("My ID: %s\n", myHexID)

	ctx, cancel := context.WithCancel(parentCtx)
	baseClient, err := client.Connect(ctx, addr, pub, priv)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("connect to router: %w", err)
	}
	slog.Debug("Connected to router")

	p := &P2PClient{
		baseClient: baseClient,
		myID:       myID,
		myHexID:    myHexID,
		ctx:        ctx,
		cancel:     cancel,
		peers:      make(map[string]*peerState),
		newPeers:   make(chan *Peer, 10),
		logger:     slog.Default(),
	}

	go p.listenForSignals(baseClient.Listen(10))

	return p, p.newPeers, nil
}

func (p *P2PClient) Close() {
	p.cancel()
}

func (p *P2PClient) ConnectWith(connectCtx context.Context, hexID string) error {
	remoteID, err := hexToID(hexID)
	if err != nil {
		return err
	}

	p.mu.Lock()
	if ps, ok := p.peers[hexID]; ok {
		p.mu.Unlock()
		select {
		case <-ps.connected:
			return nil
		case <-connectCtx.Done():
			return connectCtx.Err()
		}
	}
	p.mu.Unlock()

	err = p.initiateConnection(remoteID, hexID)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-connectCtx.Done():
			return connectCtx.Err()
		case <-ticker.C:
			p.mu.Lock()
			ps, ok := p.peers[hexID]
			p.mu.Unlock()
			if ok {
				select {
				case <-ps.connected:
					return nil
				default:
				}
			}
		}
	}
}

func (p *P2PClient) initiateConnection(remoteID [protocol.ClientIDSize]byte, hexID string) error {
	config := webrtcConfig()
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}
	slog.Debug("Created PeerConnection as initiator", "remote", hexID)

	dc, err := pc.CreateDataChannel("chat", nil)
	if err != nil {
		pc.Close()
		return fmt.Errorf("create data channel: %w", err)
	}

	inCh := make(chan string, 10)
	sendCh := make(chan string, 10)
	connected := make(chan struct{})

	ps := &peerState{
		pc:        pc,
		dc:        dc,
		inCh:      inCh,
		sendCh:    sendCh,
		connected: connected,
		close: func() {
			pc.Close()
			close(inCh)
			close(connected)
		},
	}

	p.mu.Lock()
	p.peers[hexID] = ps
	p.mu.Unlock()

	go setupDataChannel(dc, inCh, sendCh)
	go setupICECandidates(pc, p.baseClient, remoteID)
	go p.handleConnectionStateChanges(ps, hexID, connected)

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		ps.close()
		return fmt.Errorf("create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		ps.close()
		return fmt.Errorf("set local description: %w", err)
	}
	slog.Debug("Created and set local description (offer)", "remote", hexID)

	offerMsg := SignalMessage{Type: "offer", SDP: &offer}
	payload, err := json.Marshal(offerMsg)
	if err != nil {
		ps.close()
		return fmt.Errorf("marshal offer: %w", err)
	}

	reqLogID, err := generateReqLogID()
	if err != nil {
		ps.close()
		return fmt.Errorf("generate reqLogID: %w", err)
	}

	o := protocol.Outcome{
		ReqLogID:    reqLogID,
		RecipientID: remoteID,
		Payload:     payload,
	}
	respCh, err := p.baseClient.Send(o)
	if err != nil {
		ps.close()
		return fmt.Errorf("send offer: %w", err)
	}
	slog.Debug("Sent offer", "reqLogID", hex.EncodeToString(reqLogID), "recipient", hexID)

	go waitForSendResponse(respCh)

	return nil
}

func (p *P2PClient) listenForSignals(inbox <-chan protocol.Income) {
	for {
		select {
		case <-p.ctx.Done():
			return
		case in, ok := <-inbox:
			if !ok {
				return
			}
			hexSender := hex.EncodeToString(in.SenderID[:])
			slog.Debug("Received incoming message", "type", in.Type, "sender", hexSender, "reqLogID", in.HexReqLogID(), "payload_len", len(in.Payload))

			var msg SignalMessage
			if err := json.Unmarshal(in.Payload, &msg); err != nil {
				slog.Error("Failed to unmarshal signal message", "err", err)
				continue
			}
			slog.Debug("Parsed signal message", "type", msg.Type, "sender", hexSender)

			p.mu.Lock()
			ps, ok := p.peers[hexSender]
			if !ok && msg.Type == "offer" {
				ps, err := p.handleNewOffer(in.SenderID, hexSender, msg)
				if err != nil {
					slog.Error("Failed to handle new offer", "err", err, "sender", hexSender)
					p.mu.Unlock()
					continue
				}
				p.peers[hexSender] = ps
			}
			if !ok {
				slog.Warn("No peer for message", "sender", hexSender, "type", msg.Type)
				p.mu.Unlock()
				continue
			}
			pc := ps.pc
			p.mu.Unlock()

			switch msg.Type {
			case "offer":
				// Уже обработано выше
			case "answer":
				if err := pc.SetRemoteDescription(*msg.SDP); err != nil {
					slog.Error("Failed to set remote description (answer)", "err", err, "sender", hexSender)
				} else {
					slog.Debug("Set remote description (answer)", "sender", hexSender)
				}
			case "candidate":
				if msg.Candidate != nil {
					if err := pc.AddICECandidate(*msg.Candidate); err != nil {
						slog.Error("Failed to add ICE candidate", "err", err, "sender", hexSender)
					} else {
						slog.Debug("Added ICE candidate", "candidate", msg.Candidate.Candidate, "sender", hexSender)
					}
				}
			}
		}
	}
}

func (p *P2PClient) handleNewOffer(senderID [protocol.ClientIDSize]byte, hexSender string, msg SignalMessage) (*peerState, error) {
	config := webrtcConfig()
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}
	slog.Debug("Created PeerConnection as responder", "sender", hexSender)

	dcCh := make(chan *webrtc.DataChannel, 1)
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Label() == "chat" {
			dcCh <- dc
		}
	})

	if err := pc.SetRemoteDescription(*msg.SDP); err != nil {
		pc.Close()
		return nil, fmt.Errorf("set remote description: %w", err)
	}
	slog.Debug("Set remote description (offer)", "sender", hexSender)

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return nil, fmt.Errorf("set local description: %w", err)
	}
	slog.Debug("Created and set local description (answer)", "sender", hexSender)

	answerMsg := SignalMessage{Type: "answer", SDP: &answer}
	payload, err := json.Marshal(answerMsg)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("marshal answer: %w", err)
	}

	reqLogID, err := generateReqLogID()
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("generate reqLogID: %w", err)
	}

	o := protocol.Outcome{
		ReqLogID:    reqLogID,
		RecipientID: senderID,
		Payload:     payload,
	}
	respCh, err := p.baseClient.Send(o)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("send answer: %w", err)
	}
	slog.Debug("Sent answer", "reqLogID", hex.EncodeToString(reqLogID), "recipient", hexSender)

	go waitForSendResponse(respCh)

	inCh := make(chan string, 10)
	sendCh := make(chan string, 10)
	connected := make(chan struct{})

	ps := &peerState{
		pc:        pc,
		inCh:      inCh,
		sendCh:    sendCh,
		connected: connected,
		close: func() {
			pc.Close()
			close(inCh)
			close(connected)
		},
	}

	go func() {
		select {
		case dc := <-dcCh:
			setupDataChannel(dc, inCh, sendCh)
			ps.dc = dc
		case <-p.ctx.Done():
		}
	}()

	go setupICECandidates(pc, p.baseClient, senderID)
	go p.handleConnectionStateChanges(ps, hexSender, connected)

	return ps, nil
}

func (p *P2PClient) handleConnectionStateChanges(ps *peerState, hexID string, connected chan struct{}) {
	ps.pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		slog.Debug("PeerConnection state changed", "state", s, "peer", hexID)
		if s == webrtc.PeerConnectionStateConnected {
			close(connected)
			peer := &Peer{
				HexID:  hexID,
				inCh:   ps.inCh,
				sendCh: ps.sendCh,
				close:  ps.close,
			}
			select {
			case p.newPeers <- peer:
				slog.Debug("Sent new peer to channel", "peer", hexID)
			default:
				slog.Warn("New peers channel full, dropping peer", "peer", hexID)
			}
		}
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected || s == webrtc.PeerConnectionStateClosed {
			ps.close()
			p.mu.Lock()
			delete(p.peers, hexID)
			p.mu.Unlock()
		}
	})
}

func webrtcConfig() webrtc.Configuration {
	return webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
}

func hexToID(hexID string) ([protocol.ClientIDSize]byte, error) {
	b, err := hex.DecodeString(hexID)
	if err != nil {
		return [protocol.ClientIDSize]byte{}, err
	}
	if len(b) != protocol.ClientIDSize {
		return [protocol.ClientIDSize]byte{}, errors.New("invalid ID length")
	}
	var id [protocol.ClientIDSize]byte
	copy(id[:], b)
	return id, nil
}

func generateReqLogID() ([]byte, error) {
	reqLogID := make([]byte, 12)
	_, err := rand.Read(reqLogID)
	return reqLogID, err
}

// setupDataChannel и setupICECandidates как в предыдущих версиях, но адаптированные для chan string
func setupDataChannel(dc *webrtc.DataChannel, inCh chan string, sendCh chan string) {
	dc.OnOpen(func() {
		slog.Debug("Data channel opened")
		go func() {
			for msg := range sendCh {
				dc.SendText(msg)
				slog.Debug("Sent message via data channel", "msg", msg)
			}
		}()
	})
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			str := string(msg.Data)
			slog.Debug("Received message via data channel", "msg", str)
			inCh <- str
		}
	})
	dc.OnClose(func() {
		slog.Debug("Data channel closed")
		close(inCh)
	})
}

func setupICECandidates(pc *webrtc.PeerConnection, c *client.Client, remoteID [protocol.ClientIDSize]byte) {
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			slog.Debug("ICE gathering complete")
			return
		}
		slog.Debug("Got ICE candidate", "candidate", candidate.ToJSON().Candidate)

		candMsg := SignalMessage{
			Type: "candidate",
			Candidate: &webrtc.ICECandidateInit{
				Candidate:        candidate.ToJSON().Candidate,
				SDPMid:           candidate.ToJSON().SDPMid,
				SDPMLineIndex:    candidate.ToJSON().SDPMLineIndex,
				UsernameFragment: candidate.ToJSON().UsernameFragment,
			},
		}
		payload, err := json.Marshal(candMsg)
		if err != nil {
			slog.Error("Failed to marshal candidate", "err", err)
			return
		}

		reqLogID := make([]byte, 12)
		if _, err := rand.Read(reqLogID); err != nil {
			slog.Error("Failed to generate reqLogID", "err", err)
			return
		}

		o := protocol.Outcome{
			ReqLogID:    reqLogID,
			RecipientID: remoteID,
			Payload:     payload,
		}
		respCh, err := c.Send(o)
		if err != nil {
			slog.Error("Failed to send candidate", "err", err)
			return
		}
		slog.Debug("Sent candidate", "reqLogID", hex.EncodeToString(reqLogID), "recipient", hex.EncodeToString(remoteID[:]))

		go waitForSendResponse(respCh)
	})
}

func waitForSendResponse(ch <-chan protocol.Income) {
	in, ok := <-ch
	if !ok {
		slog.Debug("Response channel closed")
		return
	}
	slog.Debug("Received send response", "status", in.Status(), "reqLogID", in.HexReqLogID())
	if in.Status() != protocol.StatusSuccess {
		slog.Error("Send failed", "status", in.Status())
	}
}

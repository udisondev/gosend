package p2p

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

	"github.com/pion/webrtc/v4"
	"github.com/samber/lo"
	"github.com/udisondev/gosend/internal/client"
	"github.com/udisondev/gosend/pkg/protocol"
)

type SignalMessage struct {
	Type      string                     `json:"type"`
	SDP       *webrtc.SessionDescription `json:"sdp"`
	Candidate *webrtc.ICECandidateInit   `json:"candidate"`
}

const (
	TypeOffer     string = "offer"
	TypeAnswer    string = "answer"
	TypeCandidate string = "candidate"
)

func (p *Peer) Send(msg string) error {
	return p.dc.Send([]byte(msg))
}

type Client struct {
	rc       *client.Client
	myID     [protocol.ClientIDSize]byte
	myHexID  string
	mu       sync.Mutex
	peers    map[string]*Peer
	newPeers chan *Peer
}

func NewP2PClient(
	connectCtx context.Context,
	addr string,
	pubsign ed25519.PublicKey,
	privsign ed25519.PrivateKey,
) (*Client, error) {
	myID := [protocol.ClientIDSize]byte(pubsign)
	baseClient, err := client.Connect(connectCtx, addr, pubsign, privsign)
	if err != nil {
		return nil, fmt.Errorf("client.Connect: %w", err)
	}
	slog.Debug("Connected to router")

	p := &Client{
		rc:       baseClient,
		myID:     myID,
		peers:    make(map[string]*Peer),
		newPeers: make(chan *Peer, 10),
	}

	return p, nil
}

func (c *Client) Listen() <-chan *Peer {
	go func() {
		for in := range c.rc.Listen(100) {
			hexSender := hex.EncodeToString(in.SenderID[:])
			slog.Debug("Received incoming message", "type", in.Type, "sender", hexSender, "reqLogID", in.HexReqLogID(), "payload_len", len(in.Payload))

			var msg SignalMessage
			if err := json.Unmarshal(in.Payload, &msg); err != nil {
				slog.Error("Failed to unmarshal signal message", "err", err, "payload", string(in.Payload))
				continue
			}
			slog.Debug("Parsed signal message", "type", msg.Type, "sender", hexSender)

			switch msg.Type {
			case TypeOffer:
				c.handleNewOffer(in.SenderID, msg)
			case TypeAnswer:
				slog.Debug("Received answer")
				c.mu.Lock()
				peer, ok := c.peers[hexSender]
				c.mu.Unlock()
				if !ok {
					slog.Debug("Unknown answer")
					continue
				}

				if err := peer.pc.SetRemoteDescription(*msg.SDP); err != nil {
					slog.Error("Failed to set remote description (answer)", "err", err)
				} else {
					slog.Debug("Set remote description (answer)")
				}
			case TypeCandidate:
				c.mu.Lock()
				peer, ok := c.peers[hexSender]
				c.mu.Unlock()
				if !ok {
					slog.Debug("Unknown candidate", "sender", hexSender)
					continue
				}

				if err := peer.pc.AddICECandidate(*msg.Candidate); err != nil {
					slog.Error("Failed to add condidate", "sender", hexSender)
					continue
				}
			default:
				slog.Debug("Unknown message type", "type", msg.Type)
				continue
			}
		}
	}()

	return c.newPeers
}

func (c *Client) ConnectWith(hexID string) error {
	remoteID, err := hexToID(hexID)
	if err != nil {
		return err
	}

	return c.initiateConnection(remoteID, hexID)
}

func (c *Client) initiateConnection(peerID [protocol.ClientIDSize]byte, hexID string) (err error) {
	config := webrtcConfig()
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		pc.Close()
	}()

	dc, err := pc.CreateDataChannel("chat", nil)
	if err != nil {
		return fmt.Errorf("create data channel: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		dc.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	peer := Peer{
		id:         peerID,
		inCh:       make(chan Message, 100),
		dc:         dc,
		pc:         pc,
		disconnect: cancel,
		ctx:        ctx,
	}

	c.setupPC(pc, &peer)

	slog.Debug("Created PeerConnection as initiator", "remote", hexID)

	dc.OnOpen(func() {
		slog.Debug("Data channel opened")
		peer.inCh <- Message{
			Text: "Hello",
		}
	})
	dc.OnMessage(func(income webrtc.DataChannelMessage) {
		slog.Debug("Received message via data channel", "peer", hexID)

		var msg Message
		if err := json.Unmarshal(income.Data, &msg); err != nil {
			slog.Error("Failed to unmarshal msg", "data", income.Data)
			return
		}
	})
	dc.OnClose(func() {
		slog.Debug("Data channel closed")
	})

	c.mu.Lock()
	c.peers[hexID] = &peer
	c.mu.Unlock()

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}
	slog.Debug("Created and set local description (offer)", "remote", hexID)

	offerMsg := SignalMessage{Type: TypeOffer, SDP: &offer}
	payload, err := json.Marshal(offerMsg)
	if err != nil {
		return fmt.Errorf("marshal offer: %w", err)
	}

	reqLogID := generateReqLogID()
	o := protocol.FromClient{
		RequestID:   reqLogID,
		RecipientID: peerID,
		Payload:     payload,
	}
	respCh, err := c.rc.Send(o)
	if err != nil {
		return fmt.Errorf("send offer: %w", err)
	}

	if resp := <-respCh; !resp.IsSuccess() {
		return fmt.Errorf("send offer: %w", err)
	}

	slog.Debug("Sent offer", "reqLogID", hex.EncodeToString(reqLogID), "recipient", hexID)

	return nil
}

func (c *Client) handleNewOffer(senderID [protocol.ClientIDSize]byte, msg SignalMessage) (err error) {
	hexID := hex.EncodeToString(senderID[:])

	config := webrtcConfig()

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		pc.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	peer := Peer{
		id:         senderID,
		inCh:       make(chan Message),
		pc:         pc,
		ctx:        ctx,
		disconnect: cancel,
	}
	c.mu.Lock()
	c.peers[hexID] = &peer
	c.mu.Unlock()
	go func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.peers, hexID)
	}()

	go func() {
		<-ctx.Done()
		pc.Close()
	}()

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		peer.dc = dc
		go func() {
			<-ctx.Done()
			dc.Close()
		}()

		dc.OnMessage(func(income webrtc.DataChannelMessage) {
			slog.Debug("Received message via data channel", "peer", hexID)

			var msg Message
			if err := json.Unmarshal(income.Data, &msg); err != nil {
				slog.Error("Failed to unmarshal msg", "data", income.Data)
				return
			}

			peer.inCh <- msg
		})

		dc.OnClose(func() {
			slog.Debug("Data channel closed")
		})
	})

	c.setupPC(pc, &peer)

	if err := pc.SetRemoteDescription(*msg.SDP); err != nil {
		return fmt.Errorf("set remote description: %w", err)
	}
	slog.Debug("Set remote description (offer)", "sender", hexID)

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %w", err)
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}
	slog.Debug("Created and set local description (answer)", "sender", hexID)

	answerMsg := SignalMessage{Type: TypeAnswer, SDP: &answer}
	payload, err := json.Marshal(answerMsg)
	if err != nil {
		return fmt.Errorf("marshal answer: %w", err)
	}

	reqLogID := generateReqLogID()
	o := protocol.FromClient{
		RequestID:   reqLogID,
		RecipientID: senderID,
		Payload:     payload,
	}
	respCh, err := c.rc.Send(o)
	if err != nil {
		return fmt.Errorf("send answer: %w", err)
	}
	slog.Debug("Sent answer", "reqLogID", hex.EncodeToString(reqLogID), "recipient", hexID)

	if reply := <-respCh; !reply.IsSuccess() {
		err = fmt.Errorf("send answer: response status=%s", reply.Status())
		return
	}

	return nil
}

func (c *Client) setupPC(pc *webrtc.PeerConnection, peer *Peer) {
	hexID := peer.HexID()
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			slog.Debug("ICE gathering complete")
			return
		}
		candidate.ToJSON()
		slog.Debug("Got ICE candidate", "candidate", candidate.ToJSON().Candidate)

		candMsg := SignalMessage{
			Type:      TypeCandidate,
			Candidate: lo.ToPtr(candidate.ToJSON()),
		}
		payload, err := json.Marshal(candMsg)
		if err != nil {
			slog.Error("Failed to marshal candidate", "err", err)
			return
		}

		reqLogID := generateReqLogID()
		o := protocol.FromClient{
			RequestID:   reqLogID,
			RecipientID: peer.id,
			Payload:     payload,
		}
		respCh, err := c.rc.Send(o)
		if err != nil {
			slog.Error("Failed to send candidate", "err", err)
			return
		}
		slog.Debug("Sent candidate", "reqLogID", hex.EncodeToString(reqLogID), "recipient", hexID)

		if reply := <-respCh; !reply.IsSuccess() {
			slog.Error("Failed to send ICE candidate", "recipient", hexID)
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		slog.Debug("PeerConnection state changed", "state", s, "peer", hexID)
		if s == webrtc.PeerConnectionStateConnected {
			select {
			case c.newPeers <- peer:
				c.mu.Lock()
				c.peers[hexID] = peer
				c.mu.Unlock()

				slog.Debug("Sent new peer to channel", "peer", hexID)
			default:
				slog.Warn("New peers channel full, dropping peer", "peer", hexID)
			}
		}
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateDisconnected || s == webrtc.PeerConnectionStateClosed {
			peer.disconnect()
		}
	})
}

func hexToID(hexID string) ([protocol.ClientIDSize]byte, error) {
	b, err := hex.DecodeString(hexID)
	if err != nil {
		return [protocol.ClientIDSize]byte{}, err
	}

	if len(b) != protocol.ClientIDSize {
		return [protocol.ClientIDSize]byte{}, errors.New("invalid ID length")
	}

	return [protocol.ClientIDSize]byte(b), nil
}

func generateReqLogID() []byte {
	reqLogID := make([]byte, 12)
	rand.Read(reqLogID)
	return reqLogID
}

func webrtcConfig() webrtc.Configuration {
	return webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
}

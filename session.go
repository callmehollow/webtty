package main

import (
	"log"
	"os"

	"github.com/maxmcd/webtty/pkg/sd"
	"github.com/pions/webrtc"
	"github.com/pions/webrtc/pkg/datachannel"
	"github.com/pions/webrtc/pkg/ice"
	"golang.org/x/crypto/ssh/terminal"
)

type sessionRunner interface {
	run() error
	setStuntServers(...string)
}

type session struct {
	// mutex?
	oldTerminalState *terminal.State
	stunServers      []string
	errChan          chan error
	isTerminal       bool
	pc               *webrtc.RTCPeerConnection
	offer            sd.SessionDescription
	answer           sd.SessionDescription
	dc               *webrtc.RTCDataChannel
}

func (s *session) init() (err error) {
	s.errChan = make(chan error, 1)
	s.isTerminal = terminal.IsTerminal(int(os.Stdin.Fd()))
	return s.createPeerConnection()
}

func (s *session) cleanup() {
	if s.dc != nil {
		// TODO: check dc state?
		if err := s.dc.Send(datachannel.PayloadString{Data: []byte("quit")}); err != nil {
			log.Println(err)
		}
	}
	if s.isTerminal {
		if err := s.restoreTerminalState(); err != nil {
			log.Println(err)
		}
	}
}

func (s *session) restoreTerminalState() error {
	if s.oldTerminalState != nil {
		return terminal.Restore(int(os.Stdin.Fd()), s.oldTerminalState)
	}
	return nil
}

func (s *session) makeRawTerminal() (err error) {
	s.oldTerminalState, err = terminal.MakeRaw(int(os.Stdin.Fd()))
	return
}

func (s *session) createPeerConnection() (err error) {
	config := webrtc.RTCConfiguration{
		IceServers: []webrtc.RTCIceServer{
			{
				URLs: s.stunServers,
			},
		},
	}
	s.pc, err = webrtc.New(config)
	if err != nil {
		return
	}
	// fmt.Println(s.pc)
	// fmt.Println(s.pc.SignalingState)
	// fmt.Println(s.pc.ConnectionState)

	// if s.pc.OnDataChannel == nil {
	// 	return errors.New("Couldn't create a peerConnection")
	// }
	s.pc.OnICEConnectionStateChange = func(connectionState ice.ConnectionState) {
		log.Printf("ICE Connection State has changed: %s\n", connectionState)
	}
	return
}

func (s *session) setStuntServers(servers ...string) {
	s.stunServers = servers
}

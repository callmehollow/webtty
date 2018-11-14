package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"time"

	"github.com/kr/pty"
	"github.com/maxmcd/webtty/pkg/sd"
	"github.com/mitchellh/colorstring"
	"github.com/pions/webrtc"
	"github.com/pions/webrtc/pkg/datachannel"
)

type hostSession struct {
	session
	cmd            []string
	nonInteractive bool
	oneWay         bool
	ptmx           *os.File
	ptmxReady      bool
	tmux           bool
}

func (hs *hostSession) dataChannelOnOpen() func() {
	return func() {
		colorstring.Println("[bold]Terminal session started:")

		cmd := exec.Command(hs.cmd[0], hs.cmd[1:]...)
		var err error
		hs.ptmx, err = pty.Start(cmd)
		if err != nil {
			hs.errChan <- err
			return
		}
		hs.ptmxReady = true

		if !hs.nonInteractive {
			if err = hs.makeRawTerminal(); err != nil {
				hs.errChan <- err
				return
			}
			go func() {
				if _, err = io.Copy(hs.ptmx, os.Stdin); err != nil {
					log.Println(err)
				}
			}()
		}

		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		go func() {
			for range c {
				hs.errChan <- errors.New("sigint")
			}
		}()

		buf := make([]byte, 1024)
		for {
			nr, err := hs.ptmx.Read(buf)
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				hs.errChan <- err
				return
			}
			if !hs.nonInteractive {
				if _, err = os.Stdout.Write(buf[0:nr]); err != nil {
					hs.errChan <- err
					return
				}
			}
			if err = hs.dc.Send(datachannel.PayloadBinary{Data: buf[0:nr]}); err != nil {
				hs.errChan <- err
				return
			}
		}
	}
}

func (hs *hostSession) dataChannelOnMessage() func(payload datachannel.Payload) {
	return func(payload datachannel.Payload) {

		// OnMessage can fire before onOpen
		// Let's wait for the pty session to be ready
		for hs.ptmxReady != true {
			time.Sleep(1 * time.Millisecond)
		}

		switch p := payload.(type) {
		case *datachannel.PayloadString:
			if len(p.Data) > 2 && p.Data[0] == '[' && p.Data[1] == '"' {
				var msg []string
				err := json.Unmarshal(p.Data, &msg)
				if len(msg) == 0 {
					hs.errChan <- err
				}
				if msg[0] == "stdin" {
					toWrite := []byte(msg[1])
					if len(toWrite) == 0 {
						return
					}
					_, err := hs.ptmx.Write([]byte(msg[1]))
					if err != nil {
						hs.errChan <- err
					}
					return
				}
				if msg[0] == "set_size" {
					var size []int
					_ = json.Unmarshal(p.Data, &size)
					ws, err := pty.GetsizeFull(hs.ptmx)
					if err != nil {
						hs.errChan <- err
						return
					}
					ws.Rows = uint16(size[1])
					ws.Cols = uint16(size[2])

					if len(size) >= 5 {
						ws.X = uint16(size[3])
						ws.Y = uint16(size[4])
					}

					if err := pty.Setsize(hs.ptmx, ws); err != nil {
						hs.errChan <- err
					}
					return
				}
			}
			if string(p.Data) == "quit" {
				hs.errChan <- nil
				return
			}
			hs.errChan <- fmt.Errorf(
				`Unmatched string message: "%s"`,
				string(p.Data),
			)
		case *datachannel.PayloadBinary:
			_, err := hs.ptmx.Write(p.Data)
			if err != nil {
				hs.errChan <- err
			}
		default:
			hs.errChan <- fmt.Errorf(
				"Message with type %s from DataChannel has no payload",
				p.PayloadType())
		}
	}
}

func (hs *hostSession) onDataChannel() func(dc *webrtc.RTCDataChannel) {
	return func(dc *webrtc.RTCDataChannel) {
		hs.dc = dc
		dc.Lock()
		defer dc.Unlock()
		dc.OnOpen = hs.dataChannelOnOpen()
		dc.Onmessage = hs.dataChannelOnMessage()
	}
}

func (hs *hostSession) mustReadStdin() (string, error) {
	var input string
	fmt.Scanln(&input)
	sd, err := sd.Decode(input)
	return sd.Sdp, err
}

func (hs *hostSession) createOffer() (err error) {
	hs.pc.OnDataChannel = hs.onDataChannel()

	// Create an offer to send to the browser
	offer, err := hs.pc.CreateOffer(nil)
	if err != nil {
		return
	}
	hs.offer = sd.SessionDescription{
		Sdp: offer.Sdp,
	}
	if hs.oneWay {
		hs.offer.GenKeys()
		hs.offer.Encrypt()
		hs.offer.TenKbSiteLoc = randSeq(100)
	}
	return
}

func (hs *hostSession) run() (err error) {
	if err = hs.init(); err != nil {
		return fmt.Errorf("couldn't init session: %v", err)
	}
	colorstring.Printf("[bold]Setting up a WebTTY connection.\n\n")
	if hs.oneWay {
		colorstring.Printf(
			"Warning: One-way connections rely on a third party to connect. " +
				"More info here: https://github.com/maxmcd/webtty#one-way-connections\n\n")
	}

	if err = hs.createOffer(); err != nil {
		return fmt.Errorf("coudln't create offer: %v", err)
	}

	// Output the offer in base64 so we can paste it in browser
	colorstring.Printf("[bold]Connection ready. Here is your connection data:\n\n")
	fmt.Printf("%s\n\n", sd.Encode(hs.offer))
	colorstring.Printf(`[bold]Paste it in the terminal after the webtty command` +
		"\n[bold]Or in a browser: [reset]https://maxmcd.github.io/webtty/\n\n")

	if hs.oneWay == false {
		colorstring.Println("[bold]When you have the answer, paste it below and hit enter:")
		// Wait for the answer to be pasted
		hs.answer.Sdp, err = hs.mustReadStdin()
		if err != nil {
			return
		}
		fmt.Println("Answer recieved, connecting...")
	} else {
		body, err := pollForResponse(hs.offer.TenKbSiteLoc)
		if err != nil {
			return err
		}
		hs.answer, err = sd.Decode(body)
		if err != nil {
			return err
		}
		hs.answer.Key = hs.offer.Key
		hs.answer.Nonce = hs.offer.Nonce
		if err = hs.answer.Decrypt(); err != nil {
			return err
		}
	}

	if err = hs.setHostRemoteDescription(); err != nil {
		return fmt.Errorf("couldn't set remote description: %v", err)
	}

	return hs.wait()
}

func (hs *hostSession) setHostRemoteDescription() (err error) {
	// Set the remote SessionDescription
	answer := webrtc.RTCSessionDescription{
		Type: webrtc.RTCSdpTypeAnswer,
		Sdp:  hs.answer.Sdp,
	}
	return hs.pc.SetRemoteDescription(answer)
}

func (hs *hostSession) wait() (err error) {
	err = <-hs.errChan
	hs.cleanup()
	return
}

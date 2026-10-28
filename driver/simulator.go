package driver

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Simulator is a deterministic, in-memory GRBL-like wire endpoint. It accepts
// the same stream of bytes as a serial controller; motion commands briefly
// report Run before returning to Idle. It is intended for development and tests,
// not for validating machine behavior.
type Simulator struct {
	client net.Conn
	peer   net.Conn
	once   sync.Once
}

// SimulatorSettings is the simulator's `$$` reply: laser mode on ($32=1),
// spindle range 0-1000 ($31/$30), soft limits off ($20=0), and a 300 x 200 mm
// travel ($130/$131).
const SimulatorSettings = "$0=10\r\n$1=25\r\n$20=0\r\n$21=0\r\n$22=0\r\n$30=1000\r\n$31=0\r\n$32=1\r\n$100=80.000\r\n$101=80.000\r\n$110=6000.000\r\n$111=6000.000\r\n$130=300.000\r\n$131=200.000\r\nok\r\n"

// NewSimulator creates an in-memory controller connection implementing the
// exact io.ReadWriteCloser boundary consumed by Stream.
func NewSimulator() io.ReadWriteCloser {
	client, peer := net.Pipe()
	sim := &Simulator{client: client, peer: peer}
	go sim.serve()
	return sim
}

func (s *Simulator) Read(p []byte) (int, error)  { return s.client.Read(p) }
func (s *Simulator) Write(p []byte) (int, error) { return s.client.Write(p) }
func (s *Simulator) Close() error {
	var err error
	s.once.Do(func() { err = s.client.Close(); _ = s.peer.Close() })
	return err
}

func (s *Simulator) serve() {
	defer s.peer.Close()
	reader := bufio.NewReader(s.peer)
	state := "Idle"
	var lineBuffer strings.Builder
	var stateMu sync.Mutex
	var timer *time.Timer
	write := func(value string) bool { _, err := io.WriteString(s.peer, value); return err == nil }
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return
		}
		switch b {
		case '?':
			stateMu.Lock()
			current := state
			stateMu.Unlock()
			if !write(fmt.Sprintf("<%s|MPos:0.000,0.000,0.000|FS:0,0|WCO:0.000,0.000,0.000>\r\n", current)) {
				return
			}
		case '!':
			stateMu.Lock()
			state = "Hold:1"
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(25*time.Millisecond, func() {
				stateMu.Lock()
				if state == "Hold:1" {
					state = "Hold:0"
				}
				stateMu.Unlock()
			})
			stateMu.Unlock()
		case '~':
			stateMu.Lock()
			if state == "Hold:0" || state == "Hold:1" {
				state = "Run"
				timer = time.AfterFunc(150*time.Millisecond, func() {
					stateMu.Lock()
					if state == "Run" {
						state = "Idle"
					}
					stateMu.Unlock()
				})
			}
			stateMu.Unlock()
		case 0x18:
			stateMu.Lock()
			state = "Idle"
			if timer != nil {
				timer.Stop()
			}
			stateMu.Unlock()
		case '\n':
			line := strings.TrimSpace(lineBuffer.String())
			lineBuffer.Reset()
			if line == "" {
				if !write("ok\r\n") {
					return
				}
				continue
			}
			if line == "$$" {
				// Read-only settings dump for a laser-mode GRBL 1.1 controller.
				if !write(SimulatorSettings) {
					return
				}
				continue
			}
			if strings.HasPrefix(strings.ToUpper(line), "ERR") {
				if !write("error:2\r\n") {
					return
				}
				continue
			}
			stateMu.Lock()
			if strings.HasPrefix(strings.ToUpper(line), "G0") || strings.HasPrefix(strings.ToUpper(line), "G1") || strings.HasPrefix(strings.ToUpper(line), "G2") || strings.HasPrefix(strings.ToUpper(line), "G3") {
				state = "Run"
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(150*time.Millisecond, func() {
					stateMu.Lock()
					if state == "Run" {
						state = "Idle"
					}
					stateMu.Unlock()
				})
			}
			stateMu.Unlock()
			time.Sleep(5 * time.Millisecond)
			if !write("ok\r\n") {
				return
			}
		default:
			if b != '\r' {
				lineBuffer.WriteByte(b)
			}
		}
	}
}

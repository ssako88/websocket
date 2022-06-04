package websocket

import (
	"bytes"
	"crypto/rand"
	"log"
	"net"
	"net/http"
	"strings"
	"testing"
)

type handler struct {
	ch        chan error
	receiveCh chan int
	ws        *WebSocket
	texts     []string
	binaries  [][]byte
	closed    bool
}

var _ http.Handler = (*handler)(nil)

func newHandler() *handler {
	return &handler{
		ch:        make(chan error),
		receiveCh: make(chan int),
	}
}

func (h *handler) clear() {
	h.texts = nil
	h.binaries = nil
	h.closed = false
}

func (h *handler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	ws, err := Accept(res, req)
	if err != nil {
		h.ch <- err
		req.Body.Close()
		return
	}
	h.ws = ws
	ws.Subscribe(func(ch <-chan Packet) {
		for pkt := range ch {
			switch pkt.Opcode {
			case OpcodeText:
				h.texts = append(h.texts, string(pkt.Data))
				h.receiveCh <- 0
			case OpcodeBinary:
				h.binaries = append(h.binaries, pkt.Data)
				h.receiveCh <- 0
			}
		}
	})
	h.closed = true
	h.receiveCh <- 0
}

func TestWebSocket(t *testing.T) {
	lsn, err := net.Listen("tcp", ":12345")
	if err != nil {
		t.Error("listen failed:", err)
		return
	}
	h := newHandler()
	go func() {
		if err := http.Serve(lsn, h); err != nil {
			log.Println("serve failed:", err)
		}
	}()
	go func() {
		if err := <-h.ch; err != nil {
			t.Error("server error:", err)
			return
		}
	}()
	defer lsn.Close()

	conn, err := net.Dial("tcp", "localhost:12345")
	if err != nil {
		t.Error("dial failed:", err)
		return
	}
	request := "GET / HTTP/1.1\r\nHost: localhost:12345\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Error("failed to send to server:", err)
		return
	}
	buf := make([]byte, 65536)
	n, err := conn.Read(buf)
	if err != nil {
		t.Error("failed to receive from server:", err)
		return
	}
	response := string(buf[:n])
	expected := strings.Join([]string{
		"HTTP/1.1 101 Switching Protocols",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=",
		"",
		"",
	}, "\r\n")
	if response != expected {
		t.Errorf("response is expected:\n\n%sgot:\n\n%s", expected, response)
		return
	}

	// text
	mask := make([]byte, 4)
	rand.Read(mask)
	data := []byte{
		OpcodeText, 0x84,
		mask[0], mask[1], mask[2], mask[3],
		'a' ^ mask[0], 'b' ^ mask[1], 'c' ^ mask[2], 'd' ^ mask[3],
	}
	if _, err := conn.Write(data); err != nil {
		t.Error("failed to send text:", err)
	}
	<-h.receiveCh
	if len(h.texts) != 1 {
		t.Error("text not received:", len(h.texts))
	} else if h.texts[0] != "abcd" {
		t.Error("received incorrect text:", h.texts[0])
	}
	h.clear()

	// binary
	rand.Read(mask)
	expectedBinary := []byte{0x00, 0xff, 0x80, 0x41, 0xc7}
	data = []byte{
		OpcodeBinary, 0x85,
		mask[0], mask[1], mask[2], mask[3],
		0, 0, 0, 0, 0,
	}
	for i := 0; i < len(expectedBinary); i++ {
		data[6+i] = expectedBinary[i] ^ mask[i%4]
	}
	if _, err := conn.Write(data); err != nil {
		t.Error("failed to send binary:", err)
	}
	<-h.receiveCh
	if len(h.binaries) != 1 {
		t.Error("binary not received:", len(h.binaries))
	} else if !bytes.Equal(expectedBinary, h.binaries[0]) {
		t.Error("received incorrect binary: expected:", expectedBinary, "got:", h.binaries[0])
	}
	h.clear()

	// ping - pong
	rand.Read(mask)
	data = []byte{
		OpcodePing, 0x85,
		mask[0], mask[1], mask[2], mask[3],
		0, 0, 0, 0, 0,
	}
	expectedPong := []byte{
		OpcodePong, 5,
		0, 0, 0, 0, 0,
	}
	for i := 0; i < len(expectedBinary); i++ {
		data[6+i] = expectedBinary[i] ^ mask[i%4]
		expectedPong[2+i] = expectedBinary[i]
	}
	if _, err := conn.Write(data); err != nil {
		t.Error("failed to send ping:", err)
	}
	n, err = conn.Read(data)
	if err != nil {
		t.Error("failed to receive pong:", err)
	} else if !bytes.Equal(data[:n], expectedPong) {
		t.Error("received incorrect pong: expect:", expectedPong, "got:", buf[:n])
	}

	// server ping
	expectedPing := []byte{
		OpcodePing, 5,
		0, 0, 0, 0, 0,
	}
	copy(expectedPing[2:], expectedBinary)
	h.ws.SendPing(expectedBinary)
	n, err = conn.Read(data)
	if err != nil {
		t.Error("failed to receive ping:", err)
	} else if !bytes.Equal(data[:n], expectedPing) {
		t.Error("received incorrect ping: expect:", expectedPing, "got:", buf[:n])
	}

	data = []byte{
		OpcodeClose, 0,
	}
	if _, err := conn.Write(data); err != nil {
		t.Error("failed to send close:", err)
	}
	conn.Close()
	<-h.receiveCh
	if !h.closed {
		t.Error("close not work")
	}
}

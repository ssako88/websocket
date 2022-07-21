package websocket

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// WebSocket WebSocket 接続
type WebSocket struct {
	c           net.Conn
	subscribes  []chan<- Packet
	pongCatcher chan<- Packet
	m           *sync.Mutex
}

// WebSocket packet 定数
const (
	OpcodeContinuation = 0x80
	OpcodeText         = 0x81
	OpcodeBinary       = 0x82
	OpcodeClose        = 0x88
	OpcodePing         = 0x89
	OpcodePong         = 0x8a
)

// Packet WebSocket のパケット
type Packet struct {
	Opcode byte
	Data   []byte
}

func new(c net.Conn) *WebSocket {
	ws := &WebSocket{
		c: c,
		m: &sync.Mutex{},
	}
	go ws.listenPacket()
	return ws
}

func (ws *WebSocket) sendPacket(d *Packet) error {
	_, err := ws.c.Write(d.packPacket())
	return err
}

// Subscribe データ受信イベントを同期的に購読する
// WebSocket が切れるまでイベントを購読し続ける。
func (ws *WebSocket) Subscribe(callback func(<-chan Packet)) {
	ch := make(chan Packet, 1)
	ws.m.Lock()
	ws.subscribes = append(ws.subscribes, ch)
	ws.m.Unlock()
	defer func() {
		ws.m.Lock()
		defer ws.m.Unlock()
		for i, c := range ws.subscribes {
			if c == ch {
				ws.subscribes[i] = ws.subscribes[len(ws.subscribes)-1]
				ws.subscribes = ws.subscribes[:len(ws.subscribes)-1]
				break
			}
		}
	}()
	callback(ch)
}

// SendText 文字列を送信する
func (ws *WebSocket) SendText(str string) error {
	return ws.sendPacket(&Packet{
		Opcode: OpcodeText,
		Data:   []byte(str),
	})
}

// SendBinary バイナリデータを送信する
func (ws *WebSocket) SendBinary(data []byte) error {
	if data == nil {
		data = []byte{}
	}
	return ws.sendPacket(&Packet{
		Opcode: OpcodeBinary,
		Data:   data,
	})
}

// SendPing ping を送信する
func (ws *WebSocket) SendPing(data []byte) error {
	if data == nil {
		data = []byte{}
	}
	return ws.sendPacket(&Packet{
		Opcode: OpcodePing,
		Data:   data,
	})
}

// CheckAlive ping を送信し、pong を受け取るまで待機する。
// t でタイムアウトする時間を設定できる
func (ws *WebSocket) CheckAlive(t time.Duration) (bool, error) {
	ws.m.Lock()
	if ws.pongCatcher != nil {
		ws.m.Unlock()
		return false, errors.New("CheckAlive() already running")
	}
	ch := make(chan Packet)
	ws.pongCatcher = ch
	ws.m.Unlock()
	defer func() {
		ws.m.Lock()
		ws.pongCatcher = nil
		close(ch)
		ws.m.Unlock()
	}()
	if e := ws.SendPing(nil); e != nil {
		return false, e
	}
	ctx, cancel := context.WithTimeout(context.TODO(), t)
	defer cancel()
	select {
	case <-ctx.Done():
		return false, nil
	case <-ch:
		return true, nil
	}
}

// Shutdown close パケットを送信し、Websocket を閉じる
func (ws *WebSocket) Shutdown() error {
	ws.sendPacket(&Packet{
		Opcode: OpcodeClose,
	})
	return ws.Close()
}

// Close WebSocket を閉じる
func (ws *WebSocket) Close() error {
	ws.m.Lock()
	for _, ch := range ws.subscribes {
		close(ch)
	}
	ws.m.Unlock()
	return ws.c.Close()
}

func (ws *WebSocket) listenPacket() {
	buf := make([]byte, 1<<20)
	index := 0
	for {
		n, err := ws.c.Read(buf[index:])
		if n > 0 {
			index += n
			for {
				packet, remain, err := parsePacket(buf[:index])
				if packet != nil {
					switch packet.Opcode {
					case OpcodeContinuation:
						// not implemented
					case OpcodePing:
						ws.sendPacket(&Packet{
							Opcode: OpcodePong,
							Data:   packet.Data,
						})
					case OpcodePong:
						ws.m.Lock()
						if ws.pongCatcher != nil {
							ws.m.Unlock()
							ws.pongCatcher <- *packet
						} else {
							ws.m.Unlock()
						}
					case OpcodeClose:
						ws.Close()
						return
					case OpcodeText, OpcodeBinary:
						// notify subscribers
						func() {
							ws.m.Lock()
							defer ws.m.Unlock()
							for _, ch := range ws.subscribes {
								ch <- *packet
							}
						}()
					default:
						// ignore
					}
				}
				if len(remain) == 0 {
					index = 0
					break
				}
				index = len(remain)
				copy(buf, remain)
				if err != nil {
					break
				}
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
		if index == len(buf) {
			return
		}
	}
}

func (pkt *Packet) packPacket() []byte {
	opcode := pkt.Opcode
	data := pkt.Data
	datalen := len(data)
	var length []byte
	if datalen < 0x7e {
		length = []byte{byte(datalen)}
	} else if datalen < 0x8000 {
		length = []byte{0x7e, byte((datalen >> 8) & 0xff), byte(datalen & 0xff)}
	} else {
		length = []byte{
			0x7f, 0, 0, 0, 0,
			byte((datalen >> 24) & 0xff),
			byte((datalen >> 16) & 0xff),
			byte((datalen >> 8) & 0xff),
			byte(datalen & 0xff),
		}
	}
	ret := make([]byte, 1+len(length)+len(data))
	ret[0] = opcode
	copy(ret[1:len(length)+1], length)
	copy(ret[len(length)+1:], data)
	return ret
}

func parsePacket(raw []byte) (*Packet, []byte, error) {
	if len(raw) < 2 {
		return nil, raw, errors.New("too short bytes")
	}
	ret := &Packet{Opcode: raw[0]}
	masked := raw[1]&0x80 == 0x80
	lengthHeader := raw[1] & 0x7f
	length := 0
	var data []byte
	switch lengthHeader {
	case 0x7f:
		if len(raw) < 0x8000 {
			return nil, raw, errors.New("too short bytes (64-bit length)")
		}
		for i := 8; i >= 2; i-- {
			length = (length << 8) | int(raw[i])
		}
		data = raw[9:]
	case 0x7e:
		if len(raw) < 0x7e {
			return nil, raw, errors.New("too short bytes (16-bit length)")
		}
		length = (int(raw[2]) << 8) | int(raw[3])
		data = raw[4:]
	default:
		length = int(lengthHeader)
		data = raw[2:]
	}
	if masked {
		if len(data) < 4 {
			return nil, raw, errors.New("mask not enough")
		}
		mask := data[:4]
		data = data[4:]
		if len(data) < length {
			return nil, raw, errors.New("length is longer")
		}
		buf := make([]byte, length)
		for i := 0; i < length; i++ {
			buf[i] = data[i] ^ mask[i&3]
		}
		ret.Data = buf
	} else {
		if len(data) < length {
			return nil, raw, errors.New("length is longer")
		}
		ret.Data = data[:length]
	}
	remain := data[length:]
	return ret, remain, nil
}

package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// SecKeyTail Sec-WebSocket-Key ヘッダから Sec-WebSocket-Accept ヘッダを計算する際に
// Sec-WebSocket-Key の後ろに付加する文字列
const SecKeyTail = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket の HTTP ヘッダ
const (
	SecWebSocketKey    = "Sec-WebSocket-Key"
	SecWebSocketAccept = "Sec-WebSocket-Accept"
)

// Accept HTTP 接続を WebSocket 接続に切り替える。
// 切り替えに失敗したときは、呼び出し側が ResponseWriter.Body.Close() を呼ぶ必要がある。
// 切り替えに成功したときは、 ResponseWriter.Body.Close() を呼ばないこと。
func Accept(res http.ResponseWriter, req *http.Request) (*WebSocket, error) {
	// WebSocket 化リクエストの確認
	secKey := req.Header.Get(SecWebSocketKey)
	if len(secKey) != 24 {
		return nil, errors.New("invalid " + SecWebSocketKey)
	}

	// HTTP 通信を HTTP でなくす
	hijacker, ok := res.(http.Hijacker)
	if !ok {
		return nil, errors.New("ResponseWriter not implement Hijacker")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("failed to hijack: %w", err)
	}

	// 読み込みバッファ掃除
	// Read をブロックさせないようにして、バッファが空になるまで Read する
	if err := conn.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		log.Println("info: websocket.Accept: failed to SetReadDeadline to 1ms:", err)
	}
	emptyReadBuffer(rw)
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		log.Println("info: websocket.Accept: failed to SetReadDeadline to 0:", err)
	}

	// WebSocket 通信を確立する
	secAccept := CreateSecWebSocketAccept(secKey)
	responseData := []byte(strings.Join([]string{
		"HTTP/1.1 101 Switching Protocols",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: " + secAccept,
		"",
		"",
	}, "\r\n"))
	if _, err := conn.Write(responseData); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to write response (switching protocol): %w", err)
	}
	return new(conn), nil
}

// CreateSecWebSocketAccept Sec-WebSocket-Accept ヘッダを計算する
func CreateSecWebSocketAccept(secWebSocketKey string) string {
	dat := []byte(secWebSocketKey + SecKeyTail)
	h := sha1.New()
	h.Write(dat)
	buf := make([]byte, 28)
	base64.StdEncoding.Encode(buf, h.Sum(nil))
	return string(buf)
}

func emptyReadBuffer(rw *bufio.ReadWriter) {
	buf := make([]byte, 4096)
	for {
		n, e := rw.Read(buf)
		if n == 0 {
			break
		}
		if e != nil {
			log.Println("info: websocket.Accept: read data, but error occurred while read:", e)
		}
	}
}

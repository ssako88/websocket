# websocket
Go言語によるWebSocket実装

使い方:

```golang
func ServeHTTP(res http.ResponseWriter, req *http.Request) {
	ws, err := websocket.Accept(res, req)
	if err != nil {
		req.Body.Close()
		return
	}
	ws.Subscribe(func(ch <-chan websocket.Packet) {
		for pkt := range ch {
			if pkt.Opcode == websocket.OpcodeText {
				fmt.Println(string(pkt.Data))
			}
		}
	})
}
```

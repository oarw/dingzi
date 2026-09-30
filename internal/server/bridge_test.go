package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func bridgePair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			accepted <- conn
		}
	}))
	t.Cleanup(ts.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	select {
	case server := <-accepted:
		t.Cleanup(func() { server.Close() })
		return client, server
	case <-time.After(2 * time.Second):
		t.Fatal("websocket pair did not connect")
		return nil, nil
	}
}

func TestBridgeIdleNoticeDuringOutput(t *testing.T) {
	browser, browserSide := bridgePair(t)
	agent, agentSide := bridgePair(t)
	ended := make(chan string, 1)
	go func() { ended <- bridgeWithIdle(browserSide, agentSide, 100*time.Millisecond) }()
	written := make(chan struct{})
	go func() {
		defer close(written)
		payload := bytes.Repeat([]byte("x"), 16*1024)
		for i := 0; i < 200; i++ {
			agent.SetWriteDeadline(time.Now().Add(time.Second))
			if err := agent.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	sawOutput, sawNotice := false, false
	for {
		typ, data, err := browser.ReadMessage()
		if err != nil {
			break
		}
		if typ == websocket.BinaryMessage {
			sawOutput = true
		}
		if typ == websocket.TextMessage && bytes.Contains(data, []byte("长时间无输入")) {
			sawNotice = true
		}
	}
	select {
	case reason := <-ended:
		if reason != "idle timeout" || !sawOutput || !sawNotice {
			t.Fatalf("reason=%q output=%v notice=%v", reason, sawOutput, sawNotice)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not terminate")
	}
	select {
	case <-written:
	case <-time.After(2 * time.Second):
		t.Fatal("output writer did not stop")
	}
}

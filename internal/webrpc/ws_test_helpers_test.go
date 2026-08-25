package webrpc

import (
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func dialWS(url, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial(url, h)
}

func deadline() time.Time { return time.Now().Add(5 * time.Second) }

// waitClients waits until the hub holds n clients, so a test does not race the
// asynchronous register/unregister.
func waitClients(t *testing.T, s *Server, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		got := len(s.clients)
		s.mu.Unlock()
		if got == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("client count never reached %d", n)
}

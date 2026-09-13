package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oarw/dingzi/internal/proto"
)

func TestHTTPCheckRejectsTruncatedResponse(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("incomplete"))
	}))
	defer endpoint.Close()
	c := &Client{version: "test"}
	result := c.doHTTP(context.Background(), proto.Task{Type: proto.TaskHTTP, Target: endpoint.URL, TimeoutMS: 1000})
	if result.OK || result.Error == "" || result.StatusCode != http.StatusOK {
		t.Fatalf("truncated HTTP response treated as healthy: %+v", result)
	}
}

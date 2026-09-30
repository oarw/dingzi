package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oarw/dingzi/internal/proto"
)

func TestPingResolutionUsesTaskDeadline(t *testing.T) {
	started := make(chan struct{}, 2)
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := resolvePingTarget(ctx, resolver, "deadline-fixture.invalid")
		finished <- err
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("resolution did not preserve task deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DNS resolution outlived task cancellation")
	}
	select {
	case <-started:
	default:
		t.Fatal("fixture did not exercise DNS resolution")
	}
}

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

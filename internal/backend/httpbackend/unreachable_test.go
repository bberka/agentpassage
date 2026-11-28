package httpbackend

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

func TestStoppedServerAndImageModelExplainThemselves(t *testing.T) {
	// A port that was just free, so nothing is listening on it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	for _, tc := range []struct {
		backend, want string
		kind          backend.ErrorKind
	}{
		{"ollama", "Can't reach ollama/local at " + addr, backend.ErrKindTransient},
		{"comfyui", "sdxl is an image model and can't chat", backend.ErrKindClient},
	} {
		acct := config.AccountConfig{Backend: tc.backend, Name: "local", BaseURL: "http://" + addr + "/v1"}
		req := backendRequest("sdxl")
		_, completeErr := New().Complete(context.Background(), acct, "", req)
		streamErr := New().Stream(context.Background(), acct, "", req, nil)
		for _, err := range []error{completeErr, streamErr} {
			be, ok := backend.AsBackendError(err)
			if !ok || be.Kind != tc.kind || !strings.HasPrefix(err.Error(), tc.want) {
				t.Errorf("%s: got %v", tc.backend, err)
			}
		}
	}
}

func backendRequest(model string) backend.ChatRequest {
	return backend.ChatRequest{Model: model, Messages: []backend.Message{{Role: "user", Content: "a lake"}}}
}

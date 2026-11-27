package httpbackend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

func TestStreamEndings(t *testing.T) {
	const delta = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	const textDelta = "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"
	for name, tc := range map[string]struct {
		auth, body string
		want       backend.ErrorKind // "" means success
	}{
		"openai done":            {config.AuthBearer, delta + "data: [DONE]\n\n", ""},
		"openai finish only":     {config.AuthBearer, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", ""},
		"openai cut off":         {config.AuthBearer, delta, backend.ErrKindTransient},
		"openai empty":           {config.AuthBearer, "", backend.ErrKindTransient},
		"openai error event":     {config.AuthBearer, delta + "data: {\"error\":{\"message\":\"slow down\",\"code\":429}}\n\n", backend.ErrKindRateLimit},
		"anthropic stop":         {config.AuthAnthropic, textDelta + "event: message_stop\ndata: {}\n\n", ""},
		"anthropic cut off":      {config.AuthAnthropic, textDelta, backend.ErrKindTransient},
		"anthropic overloaded":   {config.AuthAnthropic, "event: error\ndata: {\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n", backend.ErrKindTransient},
		"anthropic rate limited": {config.AuthAnthropic, "event: error\ndata: {\"error\":{\"type\":\"rate_limit_error\",\"message\":\"busy\"}}\n\n", backend.ErrKindRateLimit},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, tc.body)
		}))
		err := New().Stream(context.Background(), config.AccountConfig{BaseURL: srv.URL, AuthStyle: tc.auth}, "", backend.ChatRequest{},
			func(backend.StreamChunk) error { return nil })
		srv.Close()
		var got backend.ErrorKind
		if be, ok := backend.AsBackendError(err); ok {
			got = be.Kind
		}
		if got != tc.want || (tc.want == "") != (err == nil) {
			t.Errorf("%s: got %v, want kind %q", name, err, tc.want)
		}
	}
}

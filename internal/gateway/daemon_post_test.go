package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bberka/agentpassage/internal/quota"
)

func TestPostDaemonRejectsErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer ts.Close()
	if _, err := postDaemon[quota.Report](context.Background(), ts.URL, "k"); err == nil {
		t.Fatal("an error reply must not decode into an empty report")
	}
}

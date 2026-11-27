package gateway_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/secrets"
)

func TestPassthroughEndpointsRedactText(t *testing.T) {
	var mu sync.Mutex
	var sent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Echo the placeholder the way rerank and image replies echo text.
		start := bytes.Index(body, []byte("{{APX_"))
		echo := ""
		if start >= 0 {
			echo = string(body[start : start+bytes.Index(body[start:], []byte("}}"))+2])
		}
		_, _ = io.WriteString(w, `{"text":"`+echo+`","big":12345678901234567}`)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "admin-key"
	cfg.Privacy = config.PrivacyConfig{Enabled: true, RedactEmails: true}
	cfg.Accounts = []config.AccountConfig{{ID: "api", Name: "main", Kind: config.KindAPIKey, Backend: "openai", Enabled: true,
		BaseURL: upstream.URL, AuthStyle: config.AuthNone, Models: []string{"m"}, MaxConcurrency: 4}}
	reg, _ := descriptors.LoadRegistry(dir)
	ts := httptest.NewServer(gateway.New(dir, cfg, secrets.NewFileOnly(dir), reg, nil).Handler())
	defer ts.Close()

	check := func(path, contentType, body string) {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer admin-key")
		req.Header.Set("Content-Type", contentType)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		mu.Lock()
		defer mu.Unlock()
		if res.StatusCode != http.StatusOK || strings.Contains(sent, "alice@example.com") || !strings.Contains(sent, "{{APX_EMAIL_1}}") {
			t.Errorf("%s: status %d, upstream got %q", path, res.StatusCode, sent)
		}
		if !strings.Contains(string(out), "alice@example.com") || !strings.Contains(string(out), "12345678901234567") {
			t.Errorf("%s: reply was not restored: %s", path, out)
		}
	}
	const jsonType = "application/json"
	check("/v1/embeddings", jsonType, `{"model":"openai/m","input":["mail alice@example.com"]}`)
	check("/v1/rerank", jsonType, `{"model":"openai/m","query":"q","documents":[{"text":"alice@example.com"}]}`)
	check("/v1/images/generations", jsonType, `{"model":"openai/m","prompt":"alice@example.com"}`)
	check("/v1/audio/speech", jsonType, `{"model":"openai/m","input":"alice@example.com"}`)

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("model", "openai/m")
	_ = mw.WriteField("prompt", "alice@example.com")
	file, _ := mw.CreateFormFile("file", "a.wav")
	_, _ = file.Write([]byte("RIFFdata"))
	_ = mw.Close()
	check("/v1/audio/transcriptions", mw.FormDataContentType(), form.String())
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(sent, "RIFFdata") {
		t.Errorf("uploaded file was lost: %q", sent)
	}
}

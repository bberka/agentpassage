package backend

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestParseDataURL(t *testing.T) {
	m, d, ok := ParseDataURL("data:image/png;base64,iVBO")
	if !ok || m != "image/png" || d != "iVBO" {
		t.Fatalf("base64 = %q %q %v", m, d, ok)
	}
	m, d, ok = ParseDataURL("data:text/plain,hi%20there")
	if !ok || m != "text/plain" || d != "aGkgdGhlcmU=" {
		t.Fatalf("plain = %q %q %v", m, d, ok)
	}
	if _, _, ok := ParseDataURL("https://x/y.png"); ok {
		t.Fatal("http URL parsed as data URL")
	}
}

func TestPromptAttachments(t *testing.T) {
	msgs := []Message{
		{Role: "user", Attachments: []Attachment{{Kind: AttachImage, Data: "a"}}},
		{Role: "assistant"},
		{Role: "user", Attachments: []Attachment{{Kind: AttachFile, Text: "b"}}},
	}
	if got := PromptAttachments(msgs, true); len(got) != 1 || got[0].Text != "b" {
		t.Fatalf("reused session = %+v", got)
	}
	if got := PromptAttachments(msgs, false); len(got) != 2 {
		t.Fatalf("new session = %+v", got)
	}
}

// Each address has a live server behind it, so only the address guard can make
// the load fail.
func TestLoadRefusesLocalAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Logf("cannot listen on %s: %v", addr, err)
			continue
		}
		var hits atomic.Int32
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		}))
		_ = srv.Listener.Close()
		srv.Listener = ln
		srv.Start()
		defer srv.Close()

		a := Attachment{Kind: AttachImage, URI: srv.URL + "/x.png"}
		if err := a.Load(t.Context()); err == nil || a.Data != "" || hits.Load() != 0 {
			t.Errorf("Load(%s) = %v, data %q, %d requests; want refusal", a.URI, err, a.Data, hits.Load())
		}
	}
}

package backend

import "testing"

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

func TestLoadRefusesLocalAddresses(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8787/admin/state", "http://192.168.1.1/x.png", "http://[::1]/x.png"} {
		a := Attachment{Kind: AttachImage, URI: u}
		if err := a.Load(t.Context()); err == nil || a.Data != "" {
			t.Errorf("Load(%s) = %v, data %q; want refusal", u, err, a.Data)
		}
	}
}

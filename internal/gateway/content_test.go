package gateway

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
)

func TestParseContentOpenAIParts(t *testing.T) {
	csv := base64.StdEncoding.EncodeToString([]byte("a,b\n1,2"))
	raw := json.RawMessage(`[
		{"type":"text","text":"look"},
		{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,QUJD"}},
		{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}},
		{"type":"input_audio","input_audio":{"data":"UklG","format":"mp3"}},
		{"type":"file","file":{"filename":"r.pdf","file_data":"data:application/pdf;base64,JVBE"}},
		{"type":"file","file":{"filename":"t.csv","file_data":"data:text/csv;base64,` + csv + `"}},
		{"type":"text","text":" here"}
	]`)
	text, atts := parseContent(raw, "")
	if text != "look here" {
		t.Fatalf("text = %q", text)
	}
	want := []backend.Attachment{
		{Kind: backend.AttachImage, MimeType: "image/jpeg", Data: "QUJD"},
		{Kind: backend.AttachImage, URI: "https://example.com/cat.png"},
		{Kind: backend.AttachAudio, MimeType: "audio/mpeg", Data: "UklG", Name: "audio.mp3"},
		{Kind: backend.AttachFile, MimeType: "application/pdf", Data: "JVBE", Name: "r.pdf"},
		{Kind: backend.AttachFile, MimeType: "text/csv", Text: "a,b\n1,2", Name: "t.csv"},
	}
	assertAttachments(t, atts, want)
}

func TestParseContentAnthropicAndResponses(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"text","text":"summarize"},
		{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBO"}},
		{"type":"document","title":"notes","source":{"type":"text","media_type":"text/plain","data":"hello"}},
		{"type":"document","source":{"type":"url","url":"https://example.com/a.pdf"}},
		{"type":"input_image","image_url":"data:image/webp;base64,UklG"},
		{"type":"input_file","filename":"x.pdf","file_data":"data:application/pdf;base64,JVBE"},
		{"type":"input_file","file_id":"file-123","filename":"y.pdf"}
	]`)
	text, atts := parseContent(raw, "\n")
	if text != "summarize" {
		t.Fatalf("text = %q", text)
	}
	assertAttachments(t, atts, []backend.Attachment{
		{Kind: backend.AttachImage, MimeType: "image/png", Data: "iVBO"},
		{Kind: backend.AttachFile, MimeType: "text/plain", Text: "hello", Name: "notes"},
		{Kind: backend.AttachFile, URI: "https://example.com/a.pdf"},
		{Kind: backend.AttachImage, MimeType: "image/webp", Data: "UklG"},
		{Kind: backend.AttachFile, MimeType: "application/pdf", Data: "JVBE", Name: "x.pdf"},
		{Kind: backend.AttachLink, URI: "file-id:file-123", Name: "y.pdf"},
	})
}

func TestParseResponsesInputKeepsAttachments(t *testing.T) {
	msgs := parseResponsesInput(json.RawMessage(`[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/i.png"}]}]`))
	if len(msgs) != 1 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].URI != "https://example.com/i.png" {
		t.Fatalf("image-only input dropped: %+v", msgs)
	}
}

func assertAttachments(t *testing.T, got, want []backend.Attachment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d attachments, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("attachment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

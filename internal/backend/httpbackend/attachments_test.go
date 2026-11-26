package httpbackend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
)

func TestPayloadsCarryAttachments(t *testing.T) {
	req := backend.ChatRequest{UpstreamModel: "m", Messages: []backend.Message{{
		Role: "user", Content: "what is this",
		Attachments: []backend.Attachment{
			{Kind: backend.AttachImage, MimeType: "image/png", Data: "iVBO"},
			{Kind: backend.AttachFile, MimeType: "application/pdf", Data: "JVBE", Name: "r.pdf"},
		},
	}}}
	oa, err := buildOpenAIPayload(req, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"image_url":{"url":"data:image/png;base64,iVBO"}`, `"file_data":"data:application/pdf;base64,JVBE"`, `"text":"what is this"`} {
		if !strings.Contains(string(oa), want) {
			t.Errorf("OpenAI payload missing %s: %s", want, oa)
		}
	}
	an, err := buildAnthropicPayload(req, false)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(an, &parsed); err != nil {
		t.Fatalf("anthropic payload: %v: %s", err, an)
	}
	var types []string
	for _, b := range parsed.Messages[0].Content {
		types = append(types, b["type"].(string))
	}
	if strings.Join(types, ",") != "text,image,document" {
		t.Fatalf("anthropic blocks = %v", types)
	}
}

package acpbackend

import (
	"os"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
)

func TestAttachmentBlocksInlineWhenAgentAccepts(t *testing.T) {
	atts := []backend.Attachment{
		{Kind: backend.AttachImage, MimeType: "image/png", Data: "iVBO"},
		{Kind: backend.AttachAudio, MimeType: "audio/mpeg", Data: "SUQz"},
		{Kind: backend.AttachFile, MimeType: "text/plain", Text: "hello", Name: "n.txt"},
		{Kind: backend.AttachLink, URI: "file:///tmp/x.go", Name: "x.go"},
	}
	blocks := attachmentBlocks(t.Context(), atts, promptCapabilities{Image: true, Audio: true, EmbeddedContext: true})
	types := blockTypes(blocks)
	if types != "image,audio,resource,resource_link" {
		t.Fatalf("block types = %s", types)
	}
	if r := blocks[2].Resource; r == nil || r.Text != "hello" || r.URI != "attachment:///n.txt" {
		t.Fatalf("resource = %+v", r)
	}
}

func TestAttachmentBlocksFallBackToFiles(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	atts := []backend.Attachment{
		{Kind: backend.AttachImage, MimeType: "image/png", Data: "aVZCTw==", Name: "shot.png"},
		{Kind: backend.AttachFile, MimeType: "text/plain", Text: "hello", Name: "n.txt"},
	}
	blocks := attachmentBlocks(t.Context(), atts, promptCapabilities{})
	if got := blockTypes(blocks); got != "resource_link,text,text" {
		t.Fatalf("block types = %s", got)
	}
	path := strings.TrimPrefix(blocks[0].URI, "file://")
	if b, err := os.ReadFile(path); err != nil || string(b) != "iVBO" {
		t.Fatalf("saved image %q: %q, %v", path, b, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved image mode %v", fi.Mode())
	}
	if !strings.Contains(blocks[2].Text, "[Attached file n.txt]\nhello") {
		t.Fatalf("text file not inlined: %q", blocks[2].Text)
	}
}

func blockTypes(blocks []promptBlock) string {
	var types []string
	for _, b := range blocks {
		types = append(types, b.Type)
	}
	return strings.Join(types, ",")
}

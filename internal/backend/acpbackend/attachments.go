package acpbackend

import (
	"context"
	"fmt"

	"github.com/bberka/agentpassage/internal/backend"
)

// attachmentBlocks turns attachments into ACP prompt blocks. Content the agent
// accepts inline (per its prompt capabilities) is sent inline; anything else
// is saved to a private temp file and sent as a resource link plus a text
// note with its path, so the agent can still open it with its file tools.
func attachmentBlocks(ctx context.Context, atts []backend.Attachment, caps promptCapabilities) []promptBlock {
	var blocks []promptBlock
	for _, a := range atts {
		loadErr := a.Load(ctx)
		switch {
		case a.Kind == backend.AttachLink || (a.Data == "" && a.Text == ""):
			if a.URI == "" {
				continue
			}
			blocks = append(blocks, promptBlock{Type: "resource_link", URI: a.URI, Name: nameOr(a.Name, a.URI), MimeType: a.MimeType})
			if loadErr != nil {
				blocks = append(blocks, promptBlock{Type: "text", Text: fmt.Sprintf("[Attached %s could not be downloaded: %v]", a.Label(), loadErr)})
			}
			continue
		case a.Kind == backend.AttachImage && caps.Image && a.Data != "":
			blocks = append(blocks, promptBlock{Type: "image", Data: a.Data, MimeType: mimeOr(a.MimeType, "image/png"), URI: httpURI(a.URI)})
			continue
		case a.Kind == backend.AttachAudio && caps.Audio && a.Data != "":
			blocks = append(blocks, promptBlock{Type: "audio", Data: a.Data, MimeType: mimeOr(a.MimeType, "audio/wav")})
			continue
		case a.Text != "":
			uri := a.URI
			if uri == "" {
				uri = "attachment:///" + nameOr(a.Name, "file.txt")
			}
			if caps.EmbeddedContext {
				blocks = append(blocks, promptBlock{Type: "resource", Resource: &embeddedResource{URI: uri, MimeType: mimeOr(a.MimeType, "text/plain"), Text: a.Text}})
			} else {
				blocks = append(blocks, promptBlock{Type: "text", Text: fmt.Sprintf("[Attached file %s]\n%s", nameOr(a.Name, uri), a.Text)})
			}
			continue
		}
		path, err := backend.SaveAttachment(a)
		if err != nil {
			blocks = append(blocks, promptBlock{Type: "text", Text: fmt.Sprintf("[Attached %s could not be saved: %v]", a.Label(), err)})
			continue
		}
		blocks = append(blocks,
			promptBlock{Type: "resource_link", URI: "file://" + path, Name: nameOr(a.Name, a.Kind), MimeType: a.MimeType},
			promptBlock{Type: "text", Text: fmt.Sprintf("[Attached %s saved at %s]", a.Label(), path)},
		)
	}
	return blocks
}

func nameOr(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func mimeOr(m, fallback string) string {
	if m != "" {
		return m
	}
	return fallback
}

func httpURI(u string) string {
	if len(u) > 4 && u[:4] == "http" {
		return u
	}
	return ""
}

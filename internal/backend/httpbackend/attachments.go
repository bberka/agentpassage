package httpbackend

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/bberka/agentpassage/internal/backend"
)

// openAIMessages renders messages for an OpenAI-compatible API, turning
// attachments into image_url, input_audio and file content parts.
func openAIMessages(msgs []backend.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 {
			var content any
			if m.Content != "" {
				content = m.Content
			}
			out = append(out, map[string]any{"role": m.Role, "content": content, "tool_calls": openAIToolCalls(m.ToolCalls)})
			continue
		}
		if m.Role == "tool" {
			out = append(out, map[string]any{"role": m.Role, "tool_call_id": m.ToolCallID, "content": m.Content})
			continue
		}
		if len(m.Attachments) == 0 {
			out = append(out, map[string]any{"role": m.Role, "content": m.Content})
			continue
		}
		parts := make([]map[string]any, 0, 1+len(m.Attachments))
		if m.Content != "" {
			parts = append(parts, map[string]any{"type": "text", "text": m.Content})
		}
		for _, a := range m.Attachments {
			parts = append(parts, openAIPart(a))
		}
		out = append(out, map[string]any{"role": m.Role, "content": parts})
	}
	return out
}

func openAIPart(a backend.Attachment) map[string]any {
	switch {
	case a.Kind == backend.AttachImage && (a.Data != "" || isHTTP(a.URI)):
		u := a.DataURL()
		if u == "" {
			u = a.URI
		}
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}}
	case a.Kind == backend.AttachAudio && a.Data != "":
		return map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": a.Data, "format": audioFormat(a.MimeType)}}
	case a.Text != "":
		return map[string]any{"type": "text", "text": textAttachment(a)}
	case a.Data != "":
		return map[string]any{"type": "file", "file": map[string]any{"filename": cmp.Or(a.Name, a.Kind), "file_data": a.DataURL()}}
	}
	return map[string]any{"type": "text", "text": linkNote(a)}
}

// anthropicContent renders one message's content for the Anthropic Messages
// API: a plain string without attachments, otherwise content blocks.
func anthropicContent(m backend.Message) any {
	if len(m.Attachments) == 0 {
		return m.Content
	}
	blocks := make([]map[string]any, 0, 1+len(m.Attachments))
	if m.Content != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
	}
	for _, a := range m.Attachments {
		blocks = append(blocks, anthropicBlock(a))
	}
	return blocks
}

func anthropicBlock(a backend.Attachment) map[string]any {
	isPDF := a.MimeType == "application/pdf"
	switch {
	case a.Kind == backend.AttachImage && a.Data != "":
		return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": cmp.Or(a.MimeType, "image/png"), "data": a.Data}}
	case a.Kind == backend.AttachImage && isHTTP(a.URI):
		return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": a.URI}}
	case isPDF && a.Data != "":
		return map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": a.Data}}
	case isPDF && isHTTP(a.URI):
		return map[string]any{"type": "document", "source": map[string]any{"type": "url", "url": a.URI}}
	case a.Text != "":
		return map[string]any{"type": "text", "text": textAttachment(a)}
	case a.Data != "":
		return map[string]any{"type": "text", "text": fmt.Sprintf("[Attached %s (%s) cannot be sent to this provider]", a.Label(), a.MimeType)}
	}
	return map[string]any{"type": "text", "text": linkNote(a)}
}

func textAttachment(a backend.Attachment) string {
	return fmt.Sprintf("[Attached file %s]\n%s", cmp.Or(a.Name, a.URI), a.Text)
}

func linkNote(a backend.Attachment) string {
	return fmt.Sprintf("[Attached %s: %s]", a.Label(), a.URI)
}

func audioFormat(mimeType string) string {
	switch m := strings.ToLower(mimeType); {
	case strings.Contains(m, "mpeg"), strings.Contains(m, "mp3"):
		return "mp3"
	case strings.HasPrefix(m, "audio/"):
		return strings.TrimPrefix(m, "audio/")
	}
	return "wav"
}

func isHTTP(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

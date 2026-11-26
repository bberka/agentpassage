package gateway

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/bberka/agentpassage/internal/backend"
)

// contentPart is one element of a message content array in any of the
// OpenAI Chat Completions, OpenAI Responses or Anthropic Messages formats.
// Their type names do not collide, so one decoder handles all three.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`

	// OpenAI chat: image_url is {"url": ...}; Responses: input_image has a
	// plain image_url string.
	ImageURL json.RawMessage `json:"image_url"`
	// OpenAI chat: input_audio {"data", "format"}.
	InputAudio *struct {
		Data   string `json:"data"`
		Format string `json:"format"`
	} `json:"input_audio"`
	// OpenAI chat: file {"file_data", "filename", "file_id"}.
	File *struct {
		FileData string `json:"file_data"`
		Filename string `json:"filename"`
		FileID   string `json:"file_id"`
	} `json:"file"`
	// Responses: input_file fields sit on the part itself.
	FileData string `json:"file_data"`
	FileURL  string `json:"file_url"`
	Filename string `json:"filename"`
	FileID   string `json:"file_id"`
	// Anthropic: image / document {"source": ...}, plus document title.
	Source *struct {
		Type      string `json:"type"` // base64, url, text
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	Title string `json:"title"`
}

// parseContent splits message content (a string or an array of parts) into
// its text and attachments. Text parts are joined with sep.
func parseContent(raw json.RawMessage, sep string) (string, []backend.Attachment) {
	if len(raw) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return string(raw), nil
	}
	var sb strings.Builder
	var atts []backend.Attachment
	for _, p := range parts {
		if a, ok := partAttachment(p); ok {
			atts = append(atts, a)
			continue
		}
		if p.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(p.Text)
		}
	}
	return sb.String(), atts
}

func partAttachment(p contentPart) (backend.Attachment, bool) {
	switch p.Type {
	case "image_url", "input_image":
		u := imageURL(p.ImageURL)
		if u == "" {
			if p.FileID == "" {
				return backend.Attachment{}, false
			}
			return backend.Attachment{Kind: backend.AttachLink, URI: "file-id:" + p.FileID}, true
		}
		return fromURL(backend.AttachImage, u, "", ""), true
	case "input_audio":
		if p.InputAudio == nil || p.InputAudio.Data == "" {
			return backend.Attachment{}, false
		}
		return backend.Attachment{Kind: backend.AttachAudio, Data: p.InputAudio.Data, MimeType: audioMime(p.InputAudio.Format), Name: "audio." + p.InputAudio.Format}, true
	case "file":
		if p.File == nil {
			return backend.Attachment{}, false
		}
		return fileAttachment(p.File.FileData, "", p.File.Filename, p.File.FileID)
	case "input_file":
		return fileAttachment(p.FileData, p.FileURL, p.Filename, p.FileID)
	case "image", "document":
		if p.Source == nil {
			return backend.Attachment{}, false
		}
		kind := backend.AttachFile
		if p.Type == "image" {
			kind = backend.AttachImage
		}
		a := backend.Attachment{Kind: kind, MimeType: p.Source.MediaType, Name: p.Title}
		switch p.Source.Type {
		case "base64":
			a.Data = p.Source.Data
		case "text":
			a.Text = p.Source.Data
		case "url":
			a.URI = p.Source.URL
		default:
			return backend.Attachment{}, false
		}
		return a, a.Data != "" || a.Text != "" || a.URI != ""
	}
	return backend.Attachment{}, false
}

func imageURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &obj)
	return obj.URL
}

// fromURL builds an attachment from a data: URL (inline content) or any other
// URL (fetched later by the backend that needs it).
func fromURL(kind, u, name, mimeType string) backend.Attachment {
	if m, data, ok := backend.ParseDataURL(u); ok {
		return backend.Attachment{Kind: kind, MimeType: m, Data: data, Name: name}
	}
	return backend.Attachment{Kind: kind, URI: u, Name: name, MimeType: mimeType}
}

func fileAttachment(fileData, fileURL, filename, fileID string) (backend.Attachment, bool) {
	var a backend.Attachment
	switch {
	case fileData != "":
		if m, data, ok := backend.ParseDataURL(fileData); ok {
			a = backend.Attachment{MimeType: m, Data: data}
		} else {
			a = backend.Attachment{Data: fileData} // bare base64
		}
	case fileURL != "":
		a = backend.Attachment{URI: fileURL}
	case fileID != "":
		// Uploaded file IDs belong to the provider; only a reference survives.
		a = backend.Attachment{Kind: backend.AttachLink, URI: "file-id:" + fileID, Name: filename}
		return a, true
	default:
		return a, false
	}
	a.Kind, a.Name = backend.AttachFile, filename
	if strings.HasPrefix(a.MimeType, "image/") {
		a.Kind = backend.AttachImage
	}
	if a.Data != "" && backend.IsTextMime(a.MimeType) {
		if text, ok := decodeText(a.Data); ok {
			a.Text, a.Data = text, ""
		}
	}
	return a, true
}

func audioMime(format string) string {
	switch strings.ToLower(format) {
	case "mp3":
		return "audio/mpeg"
	case "":
		return "audio/wav"
	default:
		return "audio/" + strings.ToLower(format)
	}
}

func decodeText(b64 string) (string, bool) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

// maxChatBodyBytes caps chat request bodies; inline images, audio and files
// arrive base64-encoded inside the JSON.
const maxChatBodyBytes = 64 << 20

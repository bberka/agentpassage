package privacy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"
)

// textFields are the request fields that carry the caller's text on the
// endpoints apx passes through: embeddings, rerank, images and audio.
var textFields = []string{"input", "query", "documents", "prompt", "instructions"}

// RedactPassthrough redacts the text fields of a JSON or multipart request that
// is forwarded to a provider as is. Uploaded files are not read.
func (s *Session) RedactPassthrough(contentType string, body []byte) ([]byte, error) {
	if s == nil {
		return body, nil
	}
	media, params, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(media, "multipart/") {
		return s.redactMultipart(body, params["boundary"])
	}
	var payload map[string]any
	if err := decodeNumbers(body, &payload); err != nil {
		return nil, err
	}
	for _, field := range textFields {
		if v, ok := payload[field]; ok {
			payload[field] = s.redactJSONValue(v)
		}
	}
	if !s.HasRedactions() {
		return body, nil
	}
	return json.Marshal(payload)
}

func (s *Session) redactMultipart(body []byte, boundary string) ([]byte, error) {
	in := multipart.NewReader(bytes.NewReader(body), boundary)
	var out bytes.Buffer
	w := multipart.NewWriter(&out)
	// The caller's Content-Type header names this boundary.
	if err := w.SetBoundary(boundary); err != nil {
		return nil, err
	}
	for {
		part, err := in.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, err
		}
		if part.FileName() == "" && part.FormName() == "prompt" {
			data = []byte(s.RedactText(string(data)))
		}
		dst, err := w.CreatePart(part.Header)
		if err != nil {
			return nil, err
		}
		if _, err := dst.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if !s.HasRedactions() {
		return body, nil
	}
	return out.Bytes(), nil
}

// RestorePassthrough puts the redacted values back into a JSON response. Other
// content, such as audio, is returned unchanged.
func (s *Session) RestorePassthrough(body []byte) []byte {
	if !s.HasRedactions() || !bytes.Contains(body, []byte(placeholderPrefix)) {
		return body
	}
	var v any
	if decodeNumbers(body, &v) != nil {
		return body
	}
	out, err := json.Marshal(s.restoreJSONValue(v))
	if err != nil {
		return body
	}
	return out
}

func (s *Session) restoreJSONValue(v any) any {
	switch x := v.(type) {
	case string:
		return s.RestoreText(x)
	case []any:
		for i := range x {
			x[i] = s.restoreJSONValue(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = s.restoreJSONValue(x[k])
		}
	}
	return v
}

// decodeNumbers decodes JSON keeping numbers exactly as written.
func decodeNumbers(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

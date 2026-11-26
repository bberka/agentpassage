package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/logbuf"
)

// handleOpenAIImages proxies OpenAI-compatible image generation (`POST /v1/images/generations`)
// through the router, pool, and HTTP API-key backend (OpenAI, OpenRouter, local Comfy/LM Studio, etc.).
func (s *Server) handleOpenAIImages(w http.ResponseWriter, r *http.Request) {
	s.proxyMediaJSON(w, r, "/images/generations")
}

// handleOpenAIAudioSpeech proxies OpenAI-compatible TTS (`POST /v1/audio/speech`)
// returning raw audio bytes (mp3/wav/opus/etc.) directly to the client.
func (s *Server) handleOpenAIAudioSpeech(w http.ResponseWriter, r *http.Request) {
	s.proxyMediaJSON(w, r, "/audio/speech")
}

// handleOpenAIAudioTranscriptions proxies OpenAI-compatible STT (`POST /v1/audio/transcriptions`),
// preserving multipart/form-data uploads (audio file + model field).
func (s *Server) handleOpenAIAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read multipart body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	_ = r.ParseMultipartForm(32 << 20)
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "model form field is required")
		return
	}
	s.proxyMediaRaw(w, r, "/audio/transcriptions", model, body, r.Header.Get("Content-Type"), start)
}

func (s *Server) proxyMediaJSON(w http.ResponseWriter, r *http.Request, upstreamSuffix string) {
	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	var meta struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &meta); err != nil || strings.TrimSpace(meta.Model) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	s.proxyMediaRaw(w, r, upstreamSuffix, strings.TrimSpace(meta.Model), body, "application/json", start)
}

func (s *Server) proxyMediaRaw(w http.ResponseWriter, r *http.Request, upstreamSuffix, model string, body []byte, contentType string, start time.Time) {
	principal := principalFromContext(r.Context())
	if principal != nil && principal.Client != nil && !principal.Client.AllowsModel(model) {
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("API key %q is not permitted to use model %q", principal.Client.Name, model))
		return
	}
	project := projectFromRequest(r)
	plan, err := s.router.ResolveForProject(model, project)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}

	// Rewrite `model` in JSON payloads to the upstream model ID (strip `<backend>/` prefix).
	sendBody := body
	if strings.Contains(strings.ToLower(contentType), "application/json") && plan.UpstreamModel != "" && plan.UpstreamModel != model {
		var obj map[string]any
		if json.Unmarshal(body, &obj) == nil {
			obj["model"] = plan.UpstreamModel
			if rewritten, err := json.Marshal(obj); err == nil {
				sendBody = rewritten
			}
		}
	}

	tried := make(map[string]bool)
	var lastErr error
	var lastAcct string
	client := &http.Client{Timeout: 120 * time.Second}

	for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
		lease, err := s.pool.Acquire(s.buildPoolSelectRequest(plan, "", tried))
		if err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		acct := lease.Account
		lastAcct = acct.Backend + "/" + acct.Name
		tried[acct.ID] = true

		if acct.Kind != config.KindAPIKey || strings.TrimSpace(acct.BaseURL) == "" {
			lease.Release()
			lastErr = fmt.Errorf("account %s does not support HTTP media endpoint %s", lastAcct, upstreamSuffix)
			continue
		}

		apiKey, _ := s.secrets.Get(acct.ID)
		upURL := strings.TrimRight(acct.BaseURL, "/") + upstreamSuffix
		upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upURL, bytes.NewReader(sendBody))
		if err != nil {
			lease.Release()
			lastErr = err
			continue
		}
		if contentType != "" {
			upReq.Header.Set("Content-Type", contentType)
		}
		if apiKey != "" && acct.AuthStyle != config.AuthNone {
			upReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := client.Do(upReq)
		lease.Release()
		if err != nil {
			lastErr = err
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			s.pool.RecordSuccess(acct.ID)
			if ct := resp.Header.Get("Content-Type"); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			w.Header().Set("X-Apx-Account", lastAcct)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(respBody)
			if s.logs != nil {
				s.logs.Add(logbuf.Entry{
					Level:         logbuf.LevelInfo,
					Kind:          "request",
					Method:        r.Method,
					Path:          r.URL.Path,
					Model:         model,
					UpstreamModel: plan.UpstreamModel,
					Project:       project,
					Account:       lastAcct,
					Status:        resp.StatusCode,
					DurationMs:    time.Since(start).Milliseconds(),
				})
			}
			return
		}
		lastErr = fmt.Errorf("upstream %s HTTP %d: %s", lastAcct, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	status := httpStatusFromError(lastErr)
	if s.logs != nil {
		s.logs.Add(logbuf.Entry{
			Level:      logbuf.LevelError,
			Kind:       "request",
			Method:     r.Method,
			Path:       r.URL.Path,
			Model:      model,
			Project:    project,
			Account:    lastAcct,
			Status:     status,
			DurationMs: time.Since(start).Milliseconds(),
			Error:      fmt.Sprint(lastErr),
		})
	}
	writeOpenAIError(w, status, "api_error", fmt.Sprint(lastErr))
}

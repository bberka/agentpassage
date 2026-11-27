package backend

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Attachment kinds.
const (
	AttachImage = "image"
	AttachAudio = "audio"
	AttachFile  = "file"
	AttachLink  = "link" // a reference to URI only, with no content
)

// MaxAttachmentBytes caps a fetched or decoded attachment.
const MaxAttachmentBytes = 32 << 20

// Attachment is a non-text part of a message: an image, audio clip or file.
// Content is in Data (base64) or Text (text files); URI alone means it still
// has to be fetched, or is only a reference.
type Attachment struct {
	Kind     string
	MimeType string
	Name     string
	Data     string
	Text     string
	URI      string
}

// PromptAttachments returns the attachments for a prompt built from messages:
// the last user message's when the session already holds earlier turns,
// otherwise every message's.
func PromptAttachments(messages []Message, reusedSession bool) []Attachment {
	if reusedSession {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				return messages[i].Attachments
			}
		}
		return nil
	}
	var out []Attachment
	for _, m := range messages {
		out = append(out, m.Attachments...)
	}
	return out
}

// HasAttachment reports whether any message carries an attachment of kind.
func HasAttachment(messages []Message, kind string) bool {
	for _, m := range messages {
		for _, a := range m.Attachments {
			if a.Kind == kind {
				return true
			}
		}
	}
	return false
}

// ParseDataURL splits "data:<mime>;base64,<data>" into its MIME type and
// base64 payload.
func ParseDataURL(s string) (mimeType, data string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(s), "data:")
	if !found {
		return "", "", false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", "", false
	}
	params := strings.Split(meta, ";")
	mimeType = params[0]
	isB64 := false
	for _, p := range params[1:] {
		if p == "base64" {
			isB64 = true
		}
	}
	if !isB64 {
		if dec, err := url.PathUnescape(payload); err == nil {
			payload = dec
		}
		return mimeType, base64.StdEncoding.EncodeToString([]byte(payload)), true
	}
	return mimeType, payload, true
}

// IsTextMime reports whether a MIME type holds text a model can read inline.
func IsTextMime(m string) bool {
	m = strings.ToLower(strings.TrimSpace(m))
	if strings.HasPrefix(m, "text/") {
		return true
	}
	for _, s := range []string{"json", "xml", "yaml", "toml", "javascript", "csv", "markdown", "x-sh"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// DataURL returns the attachment's content as a data: URL, or "" when it has
// no inline content.
func (a Attachment) DataURL() string {
	if a.Data == "" {
		return ""
	}
	return "data:" + a.mimeOrDefault() + ";base64," + a.Data
}

func (a Attachment) mimeOrDefault() string {
	if a.MimeType != "" {
		return a.MimeType
	}
	switch a.Kind {
	case AttachImage:
		return "image/png"
	case AttachAudio:
		return "audio/wav"
	}
	return "application/octet-stream"
}

// Load fills Data from an http(s) URI when the attachment has no content yet.
// Text files keep their content in Text.
func (a *Attachment) Load(ctx context.Context) error {
	if a.Data != "" || a.Text != "" || a.URI == "" {
		return nil
	}
	if !strings.HasPrefix(a.URI, "http://") && !strings.HasPrefix(a.URI, "https://") {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URI, nil)
	if err != nil {
		return err
	}
	resp, err := attachmentClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", a.URI, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxAttachmentBytes+1))
	if err != nil {
		return err
	}
	if len(body) > MaxAttachmentBytes {
		return fmt.Errorf("fetch %s: larger than %d MB", a.URI, MaxAttachmentBytes>>20)
	}
	if a.MimeType == "" {
		a.MimeType, _, _ = strings.Cut(resp.Header.Get("Content-Type"), ";")
	}
	if a.Name == "" {
		a.Name = filepath.Base(req.URL.Path)
	}
	if a.Kind == AttachFile && IsTextMime(a.MimeType) {
		a.Text = string(body)
		return nil
	}
	a.Data = base64.StdEncoding.EncodeToString(body)
	return nil
}

// attachmentClient downloads attachment URLs. It refuses loopback, private
// and link-local addresses, so a client (possibly remote, through the tunnel)
// cannot make apx fetch services on this machine or its network.
var attachmentClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
			Control: func(_, address string, _ syscall.RawConn) error {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return err
				}
				ip := net.ParseIP(host)
				if ip == nil || !isPublicIP(ip) {
					return fmt.Errorf("refusing to fetch attachment from non-public address %s", host)
				}
				return nil
			},
		}).DialContext,
	},
}

// nonPublicNets are ranges net.IP's own predicates miss: CGNAT (which
// Tailscale uses) and NAT64, which can map onto private IPv4 addresses.
var nonPublicNets = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, p := range nonPublicNets {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// attachmentDir is where SaveAttachment writes files; it is private to the user.
func attachmentDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("apx-attachments-%d", os.Getuid()))
}

const attachmentTTL = 24 * time.Hour

// SaveAttachment writes the attachment's content to a private temp file so an
// agent that cannot take it inline can read it from disk, and returns the
// path. Files older than a day are removed.
func SaveAttachment(a Attachment) (string, error) {
	var content []byte
	switch {
	case a.Data != "":
		b, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return "", fmt.Errorf("decode attachment %q: %w", a.Name, err)
		}
		content = b
	case a.Text != "":
		content = []byte(a.Text)
	default:
		return "", fmt.Errorf("attachment %q has no content", a.Name)
	}
	dir := attachmentDir()
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	pruneAttachments(dir)

	sum := sha256.Sum256(content)
	name := sanitizeFileName(a.Name)
	if name == "" {
		name = a.Kind
	}
	if filepath.Ext(name) == "" {
		if exts, _ := mime.ExtensionsByType(a.mimeOrDefault()); len(exts) > 0 {
			name += exts[0]
		}
	}
	path := filepath.Join(dir, hex.EncodeToString(sum[:6])+"-"+name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ensurePrivateDir creates dir, or accepts an existing one only if it is a
// real directory owned by this user. The shared temp directory lets another
// local user create it first.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("attachment directory %s is not a directory", dir)
	}
	if !ownedByCurrentUser(info) {
		return fmt.Errorf("attachment directory %s belongs to another user", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

func pruneAttachments(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-attachmentTTL)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func sanitizeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}

// Label names the attachment for text notes, e.g. "image photo.png".
func (a Attachment) Label() string {
	if a.Name != "" {
		return a.Kind + " " + a.Name
	}
	return a.Kind
}

package hostauth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileMapping pairs an existing host credential file with its relative destination path
// inside an isolated apx profile directory ({dataDir}/profiles/{backend}/{account}).
type FileMapping struct {
	Src     string
	RelDest string
}

// DetectedLogin describes an existing host CLI login discovered on this machine.
type DetectedLogin struct {
	Backend      string
	Label        string
	SourceDesc   string
	CanCopyFiles bool
	KeyringOnly  bool
	Files        []FileMapping
}

type backendSpec struct {
	backend string
	label   string
	detect  func(home string) (DetectedLogin, bool)
	hasAuth func(profileDir string) bool
}

var specs = []backendSpec{
	{
		backend: "claude",
		label:   "Claude Code",
		detect:  detectClaude,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, ".credentials.json"))
		},
	},
	{
		backend: "codex",
		label:   "Codex",
		detect:  detectCodex,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, "auth.json"))
		},
	},
	{
		backend: "agy",
		label:   "Google Antigravity",
		detect:  detectAgy,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, ".gemini", "antigravity-cli", "oauth_creds.json")) ||
				fileNonEmpty(filepath.Join(pdir, ".config", "antigravity", "plugin-secrets.json")) ||
				fileNonEmpty(filepath.Join(pdir, ".gemini", "antigravity-cli", "cache", "acp-auth-state.json"))
		},
	},
	{
		backend: "grok",
		label:   "Grok Build",
		detect:  detectGrok,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, ".grok", "auth.json"))
		},
	},
	{
		backend: "opencode",
		label:   "OpenCode",
		detect:  detectOpenCode,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, ".local", "share", "opencode", "auth.json"))
		},
	},
	{
		backend: "gemini",
		label:   "Gemini CLI",
		detect:  detectGemini,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, ".gemini", "oauth_creds.json"))
		},
	},
	{
		backend: "pi",
		label:   "Pi",
		detect:  detectPi,
		hasAuth: func(pdir string) bool {
			return fileNonEmpty(filepath.Join(pdir, "auth.json"))
		},
	},
}

// Detect checks whether the user already has a host login for the given backend ID.
// If homeDir is empty, os.UserHomeDir() is used.
func Detect(backend, homeDir string) (DetectedLogin, bool) {
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil || h == "" {
			return DetectedLogin{}, false
		}
		homeDir = h
	}
	b := strings.ToLower(strings.TrimSpace(backend))
	for _, s := range specs {
		if s.backend == b {
			return s.detect(homeDir)
		}
	}
	return DetectedLogin{}, false
}

// DetectAll scans standard host CLI directories for existing logins (read-only).
func DetectAll(homeDir string) []DetectedLogin {
	if homeDir == "" {
		h, err := os.UserHomeDir()
		if err != nil || h == "" {
			return nil
		}
		homeDir = h
	}
	var out []DetectedLogin
	for _, s := range specs {
		if d, ok := s.detect(homeDir); ok {
			out = append(out, d)
		}
	}
	return out
}

// ProfileHasAuth reports whether an isolated apx profile directory already holds local auth files.
func ProfileHasAuth(backend, profileDir string) bool {
	if strings.TrimSpace(profileDir) == "" {
		return false
	}
	b := strings.ToLower(strings.TrimSpace(backend))
	for _, s := range specs {
		if s.backend == b {
			return s.hasAuth(profileDir)
		}
	}
	return false
}

// CopyToProfile safely copies detected host auth files into an isolated apx profile directory
// using 0600 file permissions (and 0700 parent directories). Host files are never modified.
func CopyToProfile(d DetectedLogin, profileDir string) (int, error) {
	if !d.CanCopyFiles || len(d.Files) == 0 {
		return 0, fmt.Errorf("%s host login is keyring-backed and cannot be file-copied (use host login or sign in fresh)", d.Label)
	}
	if strings.TrimSpace(profileDir) == "" {
		return 0, fmt.Errorf("profile directory is empty")
	}
	copied := 0
	for _, fm := range d.Files {
		data, err := os.ReadFile(fm.Src)
		if err != nil {
			continue
		}
		dst := filepath.Join(profileDir, fm.RelDest)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return copied, fmt.Errorf("create dir for %s: %w", fm.RelDest, err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return copied, fmt.Errorf("write %s: %w", fm.RelDest, err)
		}
		copied++
	}
	if copied == 0 {
		return 0, fmt.Errorf("no readable host credential files found for %s", d.Label)
	}
	return copied, nil
}

func detectClaude(home string) (DetectedLogin, bool) {
	dir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if dir == "" {
		dir = filepath.Join(home, ".claude")
	}
	cred := filepath.Join(dir, ".credentials.json")
	if !fileNonEmpty(cred) {
		return DetectedLogin{}, false
	}
	files := []FileMapping{{Src: cred, RelDest: ".credentials.json"}}
	if cj := filepath.Join(dir, ".claude.json"); fileNonEmpty(cj) {
		files = append(files, FileMapping{Src: cj, RelDest: ".claude.json"})
	}
	return DetectedLogin{
		Backend:      "claude",
		Label:        "Claude Code",
		SourceDesc:   shortPath(cred, home),
		CanCopyFiles: true,
		Files:        files,
	}, true
}

func detectCodex(home string) (DetectedLogin, bool) {
	dir := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if dir == "" {
		dir = filepath.Join(home, ".codex")
	}
	auth := filepath.Join(dir, "auth.json")
	if !fileNonEmpty(auth) {
		return DetectedLogin{}, false
	}
	return DetectedLogin{
		Backend:      "codex",
		Label:        "Codex",
		SourceDesc:   shortPath(auth, home),
		CanCopyFiles: true,
		Files:        []FileMapping{{Src: auth, RelDest: "auth.json"}},
	}, true
}

func detectAgy(home string) (DetectedLogin, bool) {
	var files []FileMapping
	candidates := []struct {
		src string
		rel string
	}{
		{filepath.Join(home, ".gemini", "antigravity-cli", "oauth_creds.json"), filepath.Join(".gemini", "antigravity-cli", "oauth_creds.json")},
		{filepath.Join(home, ".gemini", "antigravity-cli", "cache", "acp-auth-state.json"), filepath.Join(".gemini", "antigravity-cli", "cache", "acp-auth-state.json")},
		{filepath.Join(home, ".config", "antigravity", "plugin-secrets.json"), filepath.Join(".config", "antigravity", "plugin-secrets.json")},
	}
	for _, c := range candidates {
		if fileNonEmpty(c.src) {
			files = append(files, FileMapping{Src: c.src, RelDest: c.rel})
		}
	}
	if len(files) > 0 {
		return DetectedLogin{
			Backend:      "agy",
			Label:        "Google Antigravity",
			SourceDesc:   shortPath(files[0].Src, home),
			CanCopyFiles: true,
			Files:        files,
		}, true
	}
	// If onboarding.json exists under ~/.gemini/antigravity-cli/cache, agy has been signed in via OS keyring.
	onb := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "onboarding.json")
	if fileNonEmpty(onb) {
		return DetectedLogin{
			Backend:      "agy",
			Label:        "Google Antigravity",
			SourceDesc:   "host keyring (~/.gemini/antigravity-cli)",
			CanCopyFiles: false,
			KeyringOnly:  true,
		}, true
	}
	return DetectedLogin{}, false
}

func detectGrok(home string) (DetectedLogin, bool) {
	auth := filepath.Join(home, ".grok", "auth.json")
	if !fileNonEmpty(auth) {
		return DetectedLogin{}, false
	}
	return DetectedLogin{
		Backend:      "grok",
		Label:        "Grok Build",
		SourceDesc:   shortPath(auth, home),
		CanCopyFiles: true,
		Files:        []FileMapping{{Src: auth, RelDest: filepath.Join(".grok", "auth.json")}},
	}, true
}

func detectOpenCode(home string) (DetectedLogin, bool) {
	auth := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if !fileNonEmpty(auth) {
		return DetectedLogin{}, false
	}
	return DetectedLogin{
		Backend:      "opencode",
		Label:        "OpenCode",
		SourceDesc:   shortPath(auth, home),
		CanCopyFiles: true,
		Files:        []FileMapping{{Src: auth, RelDest: filepath.Join(".local", "share", "opencode", "auth.json")}},
	}, true
}

func detectGemini(home string) (DetectedLogin, bool) {
	cred := filepath.Join(home, ".gemini", "oauth_creds.json")
	if !fileNonEmpty(cred) {
		return DetectedLogin{}, false
	}
	files := []FileMapping{{Src: cred, RelDest: filepath.Join(".gemini", "oauth_creds.json")}}
	if acc := filepath.Join(home, ".gemini", "google_accounts.json"); fileNonEmpty(acc) {
		files = append(files, FileMapping{Src: acc, RelDest: filepath.Join(".gemini", "google_accounts.json")})
	}
	return DetectedLogin{
		Backend:      "gemini",
		Label:        "Gemini CLI",
		SourceDesc:   shortPath(cred, home),
		CanCopyFiles: true,
		Files:        files,
	}, true
}

func detectPi(home string) (DetectedLogin, bool) {
	auth := filepath.Join(home, ".pi", "agent", "auth.json")
	if !fileNonEmpty(auth) {
		return DetectedLogin{}, false
	}
	return DetectedLogin{
		Backend:      "pi",
		Label:        "Pi",
		SourceDesc:   shortPath(auth, home),
		CanCopyFiles: true,
		Files:        []FileMapping{{Src: auth, RelDest: "auth.json"}},
	}, true
}

func fileNonEmpty(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Size() > 0
}

func shortPath(p, home string) string {
	if home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

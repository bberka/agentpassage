package descriptors

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const OfficialRegistryURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

//go:embed registry_snapshot.json
var embeddedRegistryJSON []byte

type acpRegistryFile struct {
	Version string             `json:"version"`
	Agents  []acpRegistryAgent `json:"agents"`
}

type acpRegistryAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Description  string `json:"description"`
	Repository   string `json:"repository"`
	Website      string `json:"website"`
	Distribution struct {
		NPX *struct {
			Package string   `json:"package"`
			Args    []string `json:"args"`
		} `json:"npx"`
		UVX *struct {
			Package string   `json:"package"`
			Args    []string `json:"args"`
		} `json:"uvx"`
		Binary map[string]struct {
			Archive string   `json:"archive"`
			Cmd     string   `json:"cmd"`
			Args    []string `json:"args"`
			SHA256  string   `json:"sha256"`
		} `json:"binary"`
	} `json:"distribution"`
}

func currentPlatformKey() string {
	osPart := runtime.GOOS
	archPart := runtime.GOARCH
	switch archPart {
	case "amd64":
		archPart = "x86_64"
	case "arm64":
		archPart = "aarch64"
	}
	return osPart + "-" + archPart
}

// registryIDAlias maps official ACP Registry IDs to short ergonomic acpx IDs where appropriate.
var registryIDAlias = map[string]string{
	"codex-acp":  "codex",
	"claude-acp": "claude",
	"grok-build": "grok",
	"pi-acp":     "pi",
}

func parseRegistryDescriptors(raw []byte, dataDir string) ([]ACPDescriptor, error) {
	var reg acpRegistryFile
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, err
	}
	plat := currentPlatformKey()
	out := make([]ACPDescriptor, 0, len(reg.Agents))

	for _, ag := range reg.Agents {
		// Skip duplicate legacy opencode-v2 & antigravity-acp (we have native agy + opencode).
		if ag.ID == "opencode-v2" || ag.ID == "antigravity-acp" {
			continue
		}
		id := ag.ID
		if alias, ok := registryIDAlias[ag.ID]; ok {
			id = alias
		}

		d := ACPDescriptor{
			ID:            id,
			RegistryID:    ag.ID,
			Name:          ag.Name,
			Description:   ag.Description,
			Version:       ag.Version,
			Website:       ag.Website,
			Repository:    ag.Repository,
			DefaultModels: []string{"default"},
			ProfileEnv: map[string]string{
				"XDG_CONFIG_HOME": "{profile}/.config",
			},
			FromRegistry: true,
		}

		// Prefer native binary archive when available for current OS/arch, else npx, else uvx.
		if bin, ok := ag.Distribution.Binary[plat]; ok && bin.Archive != "" {
			cleanCmd := strings.TrimPrefix(bin.Cmd, "./")
			d.BinaryArchive = bin.Archive
			d.BinarySHA256 = bin.SHA256
			d.BinaryCmd = cleanCmd
			d.BinaryArgs = bin.Args
			d.Detect = filepath.Base(cleanCmd)
			// Installed path inside acpx dataDir/bin/<id>/<cleanCmd>
			if dataDir != "" {
				localBin := filepath.Join(dataDir, "bin", id, cleanCmd)
				d.Command = append([]string{localBin}, bin.Args...)
			} else {
				d.Command = append([]string{d.Detect}, bin.Args...)
			}
		}

		if ag.Distribution.NPX != nil && ag.Distribution.NPX.Package != "" {
			pkg := stripVersionSuffix(ag.Distribution.NPX.Package)
			d.InstallPackage = pkg
			d.NPXPackage = ag.Distribution.NPX.Package
			d.NPXArgs = ag.Distribution.NPX.Args
			if len(d.Command) == 0 {
				// Can run directly via npx -y or installed global binary.
				d.Detect = guessBinFromNPX(pkg, ag.ID)
				d.Command = append([]string{"npx", "-y", ag.Distribution.NPX.Package}, ag.Distribution.NPX.Args...)
			}
		}

		if ag.Distribution.UVX != nil && ag.Distribution.UVX.Package != "" {
			d.UVXPackage = ag.Distribution.UVX.Package
			d.UVXArgs = ag.Distribution.UVX.Args
			if len(d.Command) == 0 {
				d.Detect = ag.Distribution.UVX.Package
				d.Command = append([]string{"uvx", ag.Distribution.UVX.Package}, ag.Distribution.UVX.Args...)
			}
		}

		if d.Detect == "" {
			d.Detect = id
		}

		// Known CLI-specific profile env & login commands
		switch id {
		case "codex":
			d.ProfileEnv = map[string]string{"CODEX_HOME": "{profile}"}
			d.Login = []string{"codex", "login"}
			d.Detect = "codex-acp"
			d.Command = []string{"codex-acp"}
			d.TermsNote = "Runs OpenAI Codex via official codex-acp adapter. Review OpenAI subscription terms."
		case "claude":
			d.ProfileEnv = map[string]string{"CLAUDE_CONFIG_DIR": "{profile}"}
			d.Login = []string{"claude"}
			d.Detect = "claude-agent-acp"
			d.Command = []string{"claude-agent-acp"}
			d.TermsNote = "Runs Claude Code via official claude-agent-acp adapter. Review Anthropic terms."
		case "gemini":
			d.ProfileEnv = map[string]string{
				"GEMINI_CLI_HOME": "{profile}",
				"XDG_CONFIG_HOME": "{profile}/.config",
			}
			d.Login = []string{"gemini"}
			d.Detect = "gemini"
			d.Command = []string{"gemini", "--experimental-acp"}
			d.DefaultModels = []string{"default", "gemini-2.5-pro", "gemini-2.5-flash"}
		case "opencode":
			d.ProfileEnv = map[string]string{"XDG_CONFIG_HOME": "{profile}/.config", "XDG_DATA_HOME": "{profile}/.local/share"}
			d.Detect = "opencode"
			d.Command = []string{"opencode", "acp"}
			d.Login = []string{"opencode", "auth", "login"}
		case "cursor":
			d.ProfileEnv = map[string]string{"HOME": "{profile}"}
			d.Login = []string{"agent", "login"}
		case "grok":
			d.ProfileEnv = map[string]string{
				"HOME":            "{profile}",
				"XDG_CONFIG_HOME": "{profile}/.config",
			}
			d.Login = []string{"grok", "login"}
		case "pi":
			d.ProfileEnv = map[string]string{"PI_CODING_AGENT_DIR": "{profile}"}
			d.Login = []string{"pi"}
		case "cline":
			d.ProfileEnv = map[string]string{"HOME": "{profile}"}
			d.Login = []string{"cline", "auth"}
		case "muse-code":
			d.ProfileEnv = map[string]string{"HOME": "{profile}"}
			d.Login = []string{"muse-code"}
		}

		out = append(out, d)
	}
	return out, nil
}

func stripVersionSuffix(pkg string) string {
	// e.g. "@scope/pkg@1.2.3" -> "@scope/pkg", "pkg@1.2.3" -> "pkg"
	if strings.HasPrefix(pkg, "@") {
		if idx := strings.LastIndex(pkg, "@"); idx > 0 {
			return pkg[:idx]
		}
		return pkg
	}
	if idx := strings.Index(pkg, "@"); idx > 0 {
		return pkg[:idx]
	}
	return pkg
}

func guessBinFromNPX(pkg, fallback string) string {
	if strings.Contains(pkg, "/") {
		parts := strings.Split(pkg, "/")
		return parts[len(parts)-1]
	}
	if pkg != "" {
		return pkg
	}
	return fallback
}

// SyncRegistry downloads the latest official ACP registry into <dataDir>/registry.json.
func SyncRegistry(ctx context.Context, dataDir string) (int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, OfficialRegistryURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("registry HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, err
	}
	var parsed acpRegistryFile
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("invalid registry json: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(dataDir, "registry.json"), body, 0o600); err != nil {
		return 0, err
	}
	return len(parsed.Agents), nil
}

// InstallBinary downloads, verifies SHA256, and extracts a registry binary archive into <dataDir>/bin/<id>/.
func InstallBinary(ctx context.Context, dataDir string, d ACPDescriptor, logf func(string)) (string, error) {
	if d.BinaryArchive == "" {
		return "", fmt.Errorf("no binary archive available for %s on %s", d.Name, currentPlatformKey())
	}
	destDir := filepath.Join(dataDir, "bin", d.ID)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	if logf != nil {
		logf("Downloading " + d.BinaryArchive)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BinaryArchive, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return "", err
	}

	if d.BinarySHA256 != "" {
		sum := sha256.Sum256(raw)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, d.BinarySHA256) {
			return "", fmt.Errorf("sha256 mismatch: got %s want %s", got, d.BinarySHA256)
		}
		if logf != nil {
			logf("SHA256 verified (" + got[:12] + "...)")
		}
	}

	lower := strings.ToLower(d.BinaryArchive)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		if err := extractTarGz(raw, destDir); err != nil {
			return "", err
		}
	case strings.HasSuffix(lower, ".zip"):
		if err := extractZip(raw, destDir); err != nil {
			return "", err
		}
	default:
		// Raw executable binary
		target := filepath.Join(destDir, filepath.Base(d.BinaryCmd))
		if err := os.WriteFile(target, raw, 0o755); err != nil {
			return "", err
		}
	}

	binPath := filepath.Join(destDir, strings.TrimPrefix(d.BinaryCmd, "./"))
	if _, err := os.Stat(binPath); err == nil {
		_ = os.Chmod(binPath, 0o755)
		return binPath, nil
	}
	// Fallback: search extracted dir for matching basename
	base := filepath.Base(d.BinaryCmd)
	var found string
	_ = filepath.WalkDir(destDir, func(p string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() == base {
			found = p
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	return destDir, nil
}

func extractTarGz(raw []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(destDir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0o755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0o755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)|0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			_ = f.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
}

func extractZip(raw []byte, destDir string) error {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue
		}
		target := filepath.Join(destDir, clean)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o600)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		_ = out.Close()
		_ = rc.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

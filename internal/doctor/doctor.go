package doctor

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/secrets"
)

type Status string

const (
	StatusOK   Status = "OK"
	StatusWarn Status = "WARN"
	StatusFail Status = "FAIL"
)

type Check struct {
	Name   string
	Status Status
	Detail string
}

func Run(dataDir string, cfg *config.Config, sec *secrets.Store, reg *descriptors.Registry) []Check {
	var checks []Check

	// 1. Data directory & permissions
	if info, err := os.Stat(dataDir); err == nil && info.IsDir() {
		checks = append(checks, Check{
			Name:   "Data directory",
			Status: StatusOK,
			Detail: dataDir,
		})
	} else {
		checks = append(checks, Check{
			Name:   "Data directory",
			Status: StatusFail,
			Detail: fmt.Sprintf("%s not accessible: %v", dataDir, err),
		})
	}

	// 2. Secrets backend
	if sec.UsingFileFallback() {
		checks = append(checks, Check{
			Name:   "Secret storage",
			Status: StatusWarn,
			Detail: fmt.Sprintf("Using 0600 file fallback (%s) because OS keychain is unavailable (%s)", filepath.Join(dataDir, "secrets.json"), sec.FallbackReason()),
		})
	} else {
		checks = append(checks, Check{
			Name:   "Secret storage",
			Status: StatusOK,
			Detail: "OS keychain available",
		})
	}

	// 3. Bind safety & port check
	bindStatus := StatusOK
	bindDetail := fmt.Sprintf("http://%s:%d/v1", cfg.Server.Bind, cfg.Server.Port)
	if !cfg.IsLoopbackBind() {
		bindStatus = StatusWarn
		bindDetail += " (non-loopback bind; localhost is recommended)"
	}
	if ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Server.Bind, fmt.Sprintf("%d", cfg.Server.Port))); err == nil {
		_ = ln.Close()
		bindDetail += " (port free)"
	} else {
		bindStatus = StatusWarn
		bindDetail += " (port currently in use — another acpx instance or service may be running)"
	}
	checks = append(checks, Check{Name: "Gateway bind", Status: bindStatus, Detail: bindDetail})

	// 4. Detected ACP CLIs (show all installed agents plus core primary tools)
	primary := map[string]bool{
		"agy": true, "codex": true, "claude": true, "cursor": true,
		"grok": true, "opencode": true, "muse-code": true, "pi": true,
		"cline": true, "gemini": true,
	}
	installedCount := 0
	allDetected := reg.DetectInstalled()
	for _, d := range allDetected {
		if d.Found {
			installedCount++
			checks = append(checks, Check{
				Name:   "CLI: " + d.Descriptor.Name,
				Status: StatusOK,
				Detail: fmt.Sprintf("found at %s", d.BinaryPath),
			})
		} else if primary[d.Descriptor.ID] {
			detail := fmt.Sprintf("%q not found (run `acpx install %s`)", d.Descriptor.Detect, d.Descriptor.ID)
			if d.Hint != "" {
				detail = d.Hint
			}
			checks = append(checks, Check{
				Name:   "CLI: " + d.Descriptor.Name,
				Status: StatusWarn,
				Detail: detail,
			})
		}
	}
	checks = append(checks, Check{
		Name:   "ACP Registry",
		Status: StatusOK,
		Detail: fmt.Sprintf("%d agents available (%d installed locally; press 'i' in TUI or run `acpx install`)", len(allDetected), installedCount),
	})

	// 5. Configured accounts
	if len(cfg.Accounts) == 0 {
		checks = append(checks, Check{
			Name:   "Accounts",
			Status: StatusWarn,
			Detail: "No accounts configured yet (run `acpx` or `acpx add`)",
		})
	} else {
		for _, a := range cfg.Accounts {
			name := fmt.Sprintf("Account: %s/%s", a.Backend, a.Name)
			if !a.Enabled {
				detail := "disabled"
				if a.DisabledReason != "" {
					detail += " — " + a.DisabledReason
				}
				checks = append(checks, Check{Name: name, Status: StatusWarn, Detail: detail})
				continue
			}
			if a.Kind == config.KindACP {
				pdir, err := config.ProfileDir(dataDir, a.Backend, a.Name)
				if err != nil {
					checks = append(checks, Check{Name: name, Status: StatusFail, Detail: err.Error()})
					continue
				}
				desc, ok := reg.GetACP(a.Backend)
				if !ok {
					checks = append(checks, Check{Name: name, Status: StatusFail, Detail: "unknown ACP descriptor"})
					continue
				}
				argv := desc.ExpandCommand(pdir, a.CustomCommand)
				if len(argv) == 0 {
					checks = append(checks, Check{Name: name, Status: StatusFail, Detail: "no command configured"})
					continue
				}
				if _, err := exec.LookPath(argv[0]); err != nil {
					checks = append(checks, Check{Name: name, Status: StatusFail, Detail: fmt.Sprintf("binary %q not on PATH", argv[0])})
					continue
				}
				checks = append(checks, Check{
					Name:   name,
					Status: StatusOK,
					Detail: fmt.Sprintf("ACP (%s), profile %s, perm=%s", argv[0], pdir, a.PermissionMode),
				})
			} else {
				if a.AuthStyle != config.AuthNone {
					if key, err := sec.Get(a.ID); err != nil || key == "" {
						checks = append(checks, Check{Name: name, Status: StatusFail, Detail: "API key missing in secret store"})
						continue
					}
				}
				checks = append(checks, Check{
					Name:   name,
					Status: StatusOK,
					Detail: fmt.Sprintf("HTTP (%s), %d models", a.BaseURL, len(a.Models)),
				})
			}
		}
	}

	return checks
}

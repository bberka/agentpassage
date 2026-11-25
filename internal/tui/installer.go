package tui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/descriptors"
)

type installEventMsg struct {
	line    string
	done    bool
	err     error
	toolID  string
	toolCmd string
}

type installState struct {
	queue       []descriptors.ACPDescriptor
	current     descriptors.ACPDescriptor
	commandStr  string
	lines       []string
	running     bool
	finished    bool
	err         error
	returnModal modalKind
	cancel      context.CancelFunc
	ch          chan installEventMsg
}

func (a *App) startInstall(tools []descriptors.ACPDescriptor, returnModal modalKind) tea.Cmd {
	if len(tools) == 0 {
		a.setStatus("Nothing to install.", false)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan installEventMsg, 128)
	st := &installState{
		queue:       tools,
		current:     tools[0],
		running:     true,
		returnModal: returnModal,
		cancel:      cancel,
		ch:          ch,
	}
	a.installer = st
	a.modal = modalInstalling

	go runInstallQueue(ctx, a.srv.DataDir(), tools, ch)
	return waitInstallEventCmd(ch)
}

func runInstallQueue(ctx context.Context, dataDir string, tools []descriptors.ACPDescriptor, ch chan<- installEventMsg) {
	defer close(ch)
	for idx, d := range tools {
		argv, err := d.ResolveInstallCommand()
		if err != nil {
			ch <- installEventMsg{
				toolID: d.ID,
				line:   fmt.Sprintf("✗ %s: %v", d.Name, err),
				done:   true,
				err:    err,
			}
			return
		}

		// Case 1: Native binary archive from the ACP Registry.
		if len(argv) == 0 && d.BinaryArchive != "" {
			ch <- installEventMsg{
				toolID:  d.ID,
				toolCmd: "download " + d.BinaryArchive,
				line:    fmt.Sprintf("→ [%d/%d] Downloading %s binary...", idx+1, len(tools), d.Name),
			}
			binPath, err := descriptors.InstallBinary(ctx, dataDir, d, func(msg string) {
				ch <- installEventMsg{toolID: d.ID, line: "  " + msg}
			})
			if err != nil {
				ch <- installEventMsg{
					toolID: d.ID,
					line:   fmt.Sprintf("✗ %s download failed: %v", d.Name, err),
					done:   true,
					err:    err,
				}
				return
			}
			ch <- installEventMsg{
				toolID: d.ID,
				line:   fmt.Sprintf("✓ %s installed to %s", d.Name, binPath),
			}
			continue
		}

		// Case 2: Package manager command (npm / pnpm / bun / uv / custom).
		cmdStr := strings.Join(argv, " ")
		ch <- installEventMsg{
			toolID:  d.ID,
			toolCmd: cmdStr,
			line:    fmt.Sprintf("→ [%d/%d] Installing %s: %s", idx+1, len(tools), d.Name, cmdStr),
		}

		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		if err := cmd.Start(); err != nil {
			_ = pw.Close()
			_ = pr.Close()
			ch <- installEventMsg{
				toolID: d.ID,
				line:   fmt.Sprintf("✗ failed to start %s: %v", argv[0], err),
				done:   true,
				err:    err,
			}
			return
		}

		scanDone := make(chan struct{})
		go func() {
			defer close(scanDone)
			sc := bufio.NewScanner(pr)
			sc.Buffer(make([]byte, 16*1024), 512*1024)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line != "" {
					ch <- installEventMsg{toolID: d.ID, line: "  " + line}
				}
			}
		}()

		waitErr := cmd.Wait()
		_ = pw.Close()
		<-scanDone
		_ = pr.Close()

		if waitErr != nil {
			ch <- installEventMsg{
				toolID: d.ID,
				line:   fmt.Sprintf("✗ %s install failed: %v", d.Name, waitErr),
				done:   true,
				err:    waitErr,
			}
			return
		}
		ch <- installEventMsg{
			toolID: d.ID,
			line:   fmt.Sprintf("✓ %s installed successfully.", d.Name),
		}
	}
	ch <- installEventMsg{done: true}
}

func waitInstallEventCmd(ch <-chan installEventMsg) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return installEventMsg{done: true}
		}
		return ev
	}
}

func (a *App) handleInstallEvent(ev installEventMsg) (tea.Model, tea.Cmd) {
	st := a.installer
	if st == nil {
		return a, nil
	}
	if ev.toolCmd != "" {
		st.commandStr = ev.toolCmd
	}
	if ev.line != "" {
		st.lines = append(st.lines, ev.line)
		if len(st.lines) > 200 {
			st.lines = st.lines[len(st.lines)-200:]
		}
	}
	if ev.done {
		st.running = false
		st.finished = true
		st.err = ev.err
		a.detected = a.srv.Registry().DetectInstalled()
		for _, d := range a.detected {
			if d.Found {
				a.detectSelected[d.Descriptor.ID] = true
			}
		}
		if ev.err != nil {
			a.setStatus("Install failed: "+ev.err.Error(), true)
		} else {
			a.setStatus("ACP tool installed and detected!", false)
		}
		return a, nil
	}
	return a, waitInstallEventCmd(st.ch)
}

func (a *App) updateInstallModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := a.installer
	if st == nil {
		a.modal = modalNone
		return a, nil
	}
	switch msg.String() {
	case "esc", "q":
		if st.running && st.cancel != nil {
			st.cancel()
			st.running = false
			st.finished = true
			st.lines = append(st.lines, "✗ Cancelled by user.")
			a.setStatus("Install cancelled", true)
			return a, nil
		}
		ret := st.returnModal
		a.installer = nil
		a.modal = ret
		return a, nil
	case "enter":
		if !st.running {
			ret := st.returnModal
			a.installer = nil
			a.modal = ret
			return a, nil
		}
	}
	return a, nil
}

func (a *App) viewInstallModal() string {
	st := a.installer
	if st == nil {
		return ""
	}
	cardW := clamp(a.width-12, 54, 88)
	innerW := cardW - 6

	spinFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	statusLine := a.styles.StateReady.Render("✓ Completed")
	if st.running {
		frame := spinFrames[a.spinTick%len(spinFrames)]
		statusLine = a.styles.StateBusy.Render(frame + " Installing inside apx (gateway stays running)...")
	} else if st.err != nil {
		statusLine = a.styles.StateErr.Render("✗ Failed: " + truncate(st.err.Error(), innerW-12))
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("ACP Tool Installer")+"   "+statusLine)
	if st.commandStr != "" {
		lines = append(lines, a.styles.Muted.Render("$ "+truncate(st.commandStr, innerW-2)))
	}
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	maxOut := clamp(a.height-16, 5, 14)
	start := max(len(st.lines)-maxOut, 0)
	shown := st.lines[start:]
	for _, l := range shown {
		style := a.styles.Muted
		if strings.HasPrefix(l, "✓") {
			style = a.styles.StateReady
		} else if strings.HasPrefix(l, "✗") {
			style = a.styles.StateErr
		} else if strings.HasPrefix(l, "→") {
			style = a.styles.Highlight
		}
		lines = append(lines, style.Render(truncate(l, innerW)))
	}
	for i := len(shown); i < maxOut; i++ {
		lines = append(lines, "")
	}

	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	if st.running {
		lines = append(lines, a.styles.KeyHint("esc", "cancel install"))
	} else {
		lines = append(lines, a.styles.KeyHint("enter", "continue")+"   "+a.styles.KeyHint("esc", "close"))
	}
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}

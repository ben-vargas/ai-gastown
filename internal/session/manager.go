// Package session provides polecat session lifecycle management.
package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/claude"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Common errors
var (
	ErrSessionRunning  = errors.New("session already running")
	ErrSessionNotFound = errors.New("session not found")
	ErrPolecatNotFound = errors.New("polecat not found")
)

// Manager handles polecat session lifecycle.
type Manager struct {
	tmux *tmux.Tmux
	rig  *rig.Rig
}

// NewManager creates a new session manager for a rig.
func NewManager(t *tmux.Tmux, r *rig.Rig) *Manager {
	return &Manager{
		tmux: t,
		rig:  r,
	}
}

// StartOptions configures session startup.
type StartOptions struct {
	// WorkDir overrides the default working directory (polecat clone dir).
	WorkDir string

	// Issue is an optional issue ID to work on.
	Issue string

	// Command overrides the default "claude" command.
	Command string

	// Account specifies the account handle to use (overrides default).
	Account string

	// ClaudeConfigDir is resolved CLAUDE_CONFIG_DIR for the account.
	// If set, this is injected as an environment variable.
	ClaudeConfigDir string
}

// Info contains information about a running session.
type Info struct {
	// Polecat is the polecat name.
	Polecat string `json:"polecat"`

	// SessionID is the tmux session identifier.
	SessionID string `json:"session_id"`

	// Running indicates if the session is currently active.
	Running bool `json:"running"`

	// RigName is the rig this session belongs to.
	RigName string `json:"rig_name"`

	// Attached indicates if someone is attached to the session.
	Attached bool `json:"attached,omitempty"`

	// Created is when the session was created.
	Created time.Time `json:"created,omitempty"`

	// Windows is the number of tmux windows.
	Windows int `json:"windows,omitempty"`

	// LastActivity is when the session last had activity.
	LastActivity time.Time `json:"last_activity,omitempty"`
}

// SessionName generates the tmux session name for a polecat.
func (m *Manager) SessionName(polecat string) string {
	return fmt.Sprintf("gt-%s-%s", m.rig.Name, polecat)
}

// polecatDir returns the working directory for a polecat.
func (m *Manager) polecatDir(polecat string) string {
	return filepath.Join(m.rig.Path, "polecats", polecat)
}

// hasPolecat checks if the polecat exists in this rig.
func (m *Manager) hasPolecat(polecat string) bool {
	// Check filesystem directly to handle newly-created polecats
	polecatPath := m.polecatDir(polecat)
	info, err := os.Stat(polecatPath)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Start creates and starts a new session for a polecat.
func (m *Manager) Start(polecat string, opts StartOptions) error {
	if !m.hasPolecat(polecat) {
		return fmt.Errorf("%w: %s", ErrPolecatNotFound, polecat)
	}

	sessionID := m.SessionName(polecat)

	// Check if session already exists
	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		return fmt.Errorf("%w: %s", ErrSessionRunning, sessionID)
	}

	// Determine working directory
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = m.polecatDir(polecat)
	}

	// Ensure Claude settings exist (autonomous role needs mail in SessionStart)
	if err := claude.EnsureSettingsForRole(workDir, "polecat"); err != nil {
		return fmt.Errorf("ensuring Claude settings: %w", err)
	}

	// Create session
	if err := m.tmux.NewSession(sessionID, workDir); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	// Set environment (non-fatal: session works without these)
	_ = m.tmux.SetEnvironment(sessionID, "GT_RIG", m.rig.Name)
	_ = m.tmux.SetEnvironment(sessionID, "GT_POLECAT", polecat)

	// Set CLAUDE_CONFIG_DIR for account selection (non-fatal)
	if opts.ClaudeConfigDir != "" {
		_ = m.tmux.SetEnvironment(sessionID, "CLAUDE_CONFIG_DIR", opts.ClaudeConfigDir)
	}

	// CRITICAL: Set beads environment for worktree polecats (non-fatal: session works without)
	// Polecats share the rig's beads directory (at rig root, not mayor/rig)
	// BEADS_NO_DAEMON=1 prevents daemon from committing to wrong branch
	beadsDir := filepath.Join(m.rig.Path, ".beads")
	_ = m.tmux.SetEnvironment(sessionID, "BEADS_DIR", beadsDir)
	_ = m.tmux.SetEnvironment(sessionID, "BEADS_NO_DAEMON", "1")
	_ = m.tmux.SetEnvironment(sessionID, "BEADS_AGENT_NAME", fmt.Sprintf("%s/%s", m.rig.Name, polecat))

	// Apply theme (non-fatal: theming failure doesn't affect operation)
	theme := tmux.AssignTheme(m.rig.Name)
	_ = m.tmux.ConfigureGasTownSession(sessionID, theme, m.rig.Name, polecat, "polecat")

	// Set pane-died hook for crash detection (non-fatal)
	agentID := fmt.Sprintf("%s/%s", m.rig.Name, polecat)
	_ = m.tmux.SetPaneDiedHook(sessionID, agentID)

	// Send initial command with env vars exported inline
	// NOTE: tmux SetEnvironment only affects NEW panes, not the current shell.
	// We must export GT_ROLE, GT_RIG, GT_POLECAT inline for Claude to detect identity.
	command := opts.Command
	if command == "" {
		// Polecats run with full permissions - Gas Town is for grownups
		// Export env vars inline so Claude's role detection works
		bdActor := fmt.Sprintf("%s/polecats/%s", m.rig.Name, polecat)
		command = fmt.Sprintf("export GT_ROLE=polecat GT_RIG=%s GT_POLECAT=%s BD_ACTOR=%s && claude --dangerously-skip-permissions",
			m.rig.Name, polecat, bdActor)
	}
	if err := m.tmux.SendKeys(sessionID, command); err != nil {
		return fmt.Errorf("sending command: %w", err)
	}

	// NOTE: No issue injection needed here. Work assignments are sent via mail
	// before session start, and the SessionStart hook runs gt prime + mail check
	// which shows the polecat its assignment.

	return nil
}

// Stop terminates a polecat session.
// If force is true, skips graceful shutdown and kills immediately.
func (m *Manager) Stop(polecat string, force bool) error {
	sessionID := m.SessionName(polecat)

	// Check if session exists
	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrSessionNotFound
	}

	// Sync beads before shutdown to preserve any changes
	// Run in the polecat's worktree directory
	if !force {
		polecatDir := m.polecatDir(polecat)
		if err := m.syncBeads(polecatDir); err != nil {
			// Non-fatal - log and continue with shutdown
			fmt.Printf("Warning: beads sync failed: %v\n", err)
		}
	}

	// Try graceful shutdown first (unless forced, best-effort interrupt)
	if !force {
		_ = m.tmux.SendKeysRaw(sessionID, "C-c")
		time.Sleep(100 * time.Millisecond)
	}

	// Kill the session
	if err := m.tmux.KillSession(sessionID); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	return nil
}

// syncBeads runs bd sync in the given directory.
func (m *Manager) syncBeads(workDir string) error {
	cmd := exec.Command("bd", "sync")
	cmd.Dir = workDir
	return cmd.Run()
}

// IsRunning checks if a polecat session is active.
func (m *Manager) IsRunning(polecat string) (bool, error) {
	sessionID := m.SessionName(polecat)
	return m.tmux.HasSession(sessionID)
}

// Status returns detailed status for a polecat session.
func (m *Manager) Status(polecat string) (*Info, error) {
	sessionID := m.SessionName(polecat)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}

	info := &Info{
		Polecat:   polecat,
		SessionID: sessionID,
		Running:   running,
		RigName:   m.rig.Name,
	}

	if !running {
		return info, nil
	}

	// Get detailed session info
	tmuxInfo, err := m.tmux.GetSessionInfo(sessionID)
	if err != nil {
		// Non-fatal - return basic info
		return info, nil
	}

	info.Attached = tmuxInfo.Attached
	info.Windows = tmuxInfo.Windows

	// Parse created time from tmux format (e.g., "Thu Dec 19 10:30:00 2025")
	if tmuxInfo.Created != "" {
		// Try common tmux date formats
		formats := []string{
			"Mon Jan 2 15:04:05 2006",
			"Mon Jan _2 15:04:05 2006",
			time.ANSIC,
			time.UnixDate,
		}
		for _, format := range formats {
			if t, err := time.Parse(format, tmuxInfo.Created); err == nil {
				info.Created = t
				break
			}
		}
	}

	// Parse activity time (unix timestamp from tmux)
	if tmuxInfo.Activity != "" {
		var activityUnix int64
		if _, err := fmt.Sscanf(tmuxInfo.Activity, "%d", &activityUnix); err == nil && activityUnix > 0 {
			info.LastActivity = time.Unix(activityUnix, 0)
		}
	}

	return info, nil
}

// List returns information about all sessions for this rig.
func (m *Manager) List() ([]Info, error) {
	sessions, err := m.tmux.ListSessions()
	if err != nil {
		return nil, err
	}

	prefix := fmt.Sprintf("gt-%s-", m.rig.Name)
	var infos []Info

	for _, sessionID := range sessions {
		if !strings.HasPrefix(sessionID, prefix) {
			continue
		}

		polecat := strings.TrimPrefix(sessionID, prefix)
		infos = append(infos, Info{
			Polecat:   polecat,
			SessionID: sessionID,
			Running:   true,
			RigName:   m.rig.Name,
		})
	}

	return infos, nil
}

// Attach attaches to a polecat session.
func (m *Manager) Attach(polecat string) error {
	sessionID := m.SessionName(polecat)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrSessionNotFound
	}

	return m.tmux.AttachSession(sessionID)
}

// Capture returns the recent output from a polecat session.
func (m *Manager) Capture(polecat string, lines int) (string, error) {
	sessionID := m.SessionName(polecat)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return "", ErrSessionNotFound
	}

	return m.tmux.CapturePane(sessionID, lines)
}

// Inject sends a message to a polecat session.
// Uses a longer debounce delay for large messages to ensure paste completes.
func (m *Manager) Inject(polecat, message string) error {
	sessionID := m.SessionName(polecat)

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrSessionNotFound
	}

	// Use longer debounce for large messages (spawn context can be 1KB+)
	// Claude needs time to process paste before Enter is sent
	// Scale delay based on message size: 200ms base + 100ms per KB
	debounceMs := 200 + (len(message)/1024)*100
	if debounceMs > 1500 {
		debounceMs = 1500 // Cap at 1.5s for large pastes
	}

	return m.tmux.SendKeysDebounced(sessionID, message, debounceMs)
}

// StopAll terminates all sessions for this rig.
func (m *Manager) StopAll(force bool) error {
	infos, err := m.List()
	if err != nil {
		return err
	}

	var lastErr error
	for _, info := range infos {
		if err := m.Stop(info.Polecat, force); err != nil {
			lastErr = err
		}
	}

	return lastErr
}

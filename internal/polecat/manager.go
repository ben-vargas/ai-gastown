package polecat

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

// Common errors
var (
	ErrPolecatExists     = errors.New("polecat already exists")
	ErrPolecatNotFound   = errors.New("polecat not found")
	ErrHasChanges        = errors.New("polecat has uncommitted changes")
	ErrHasUncommittedWork = errors.New("polecat has uncommitted work")
)

// UncommittedWorkError provides details about uncommitted work.
type UncommittedWorkError struct {
	PolecatName string
	Status      *git.UncommittedWorkStatus
}

func (e *UncommittedWorkError) Error() string {
	return fmt.Sprintf("polecat %s has uncommitted work: %s", e.PolecatName, e.Status.String())
}

func (e *UncommittedWorkError) Unwrap() error {
	return ErrHasUncommittedWork
}

// Manager handles polecat lifecycle.
type Manager struct {
	rig      *rig.Rig
	git      *git.Git
	beads    *beads.Beads
	namePool *NamePool
}

// NewManager creates a new polecat manager.
func NewManager(r *rig.Rig, g *git.Git) *Manager {
	// Use the rig root for beads operations (rig-level beads at .beads/)
	rigPath := r.Path

	// Try to load rig settings for namepool config
	settingsPath := filepath.Join(r.Path, "settings", "config.json")
	var pool *NamePool

	settings, err := config.LoadRigSettings(settingsPath)
	if err == nil && settings.Namepool != nil {
		// Use configured namepool settings
		pool = NewNamePoolWithConfig(
			r.Path,
			r.Name,
			settings.Namepool.Style,
			settings.Namepool.Names,
			settings.Namepool.MaxBeforeNumbering,
		)
	} else {
		// Use defaults
		pool = NewNamePool(r.Path, r.Name)
	}
	_ = pool.Load() // non-fatal: state file may not exist for new rigs

	return &Manager{
		rig:      r,
		git:      g,
		beads:    beads.New(rigPath),
		namePool: pool,
	}
}

// assigneeID returns the beads assignee identifier for a polecat.
// Format: "rig/polecatName" (e.g., "gastown/Toast")
func (m *Manager) assigneeID(name string) string {
	return fmt.Sprintf("%s/%s", m.rig.Name, name)
}

// agentBeadID returns the agent bead ID for a polecat.
// Format: "gt-polecat-<rig>-<name>" (e.g., "gt-polecat-gastown-Toast")
func (m *Manager) agentBeadID(name string) string {
	return fmt.Sprintf("gt-polecat-%s-%s", m.rig.Name, name)
}

// repoBase returns the git directory and Git object to use for worktree operations.
// Prefers the shared bare repo (.repo.git) if it exists, otherwise falls back to mayor/rig.
// The bare repo architecture allows all worktrees (refinery, polecats) to share branch visibility.
func (m *Manager) repoBase() (*git.Git, error) {
	// First check for shared bare repo (new architecture)
	bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
	if info, err := os.Stat(bareRepoPath); err == nil && info.IsDir() {
		// Bare repo exists - use it
		return git.NewGitWithDir(bareRepoPath, ""), nil
	}

	// Fall back to mayor/rig (legacy architecture)
	mayorPath := filepath.Join(m.rig.Path, "mayor", "rig")
	if _, err := os.Stat(mayorPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("no repo base found (neither .repo.git nor mayor/rig exists)")
	}
	return git.NewGit(mayorPath), nil
}

// polecatDir returns the directory for a polecat.
func (m *Manager) polecatDir(name string) string {
	return filepath.Join(m.rig.Path, "polecats", name)
}

// exists checks if a polecat exists.
func (m *Manager) exists(name string) bool {
	_, err := os.Stat(m.polecatDir(name))
	return err == nil
}

// Add creates a new polecat as a git worktree from the repo base.
// Uses the shared bare repo (.repo.git) if available, otherwise mayor/rig.
// This is much faster than a full clone and shares objects with all worktrees.
// Polecat state is derived from beads assignee field, not state.json.
//
// Branch naming: Each polecat run gets a unique branch (polecat/<name>-<timestamp>).
// This prevents drift issues from stale branches and ensures a clean starting state.
// Old branches are ephemeral and never pushed to origin.
func (m *Manager) Add(name string) (*Polecat, error) {
	if m.exists(name) {
		return nil, ErrPolecatExists
	}

	polecatPath := m.polecatDir(name)
	// Unique branch per run - prevents drift from stale branches
	branchName := fmt.Sprintf("polecat/%s-%d", name, time.Now().UnixMilli())

	// Create polecats directory if needed
	polecatsDir := filepath.Join(m.rig.Path, "polecats")
	if err := os.MkdirAll(polecatsDir, 0755); err != nil {
		return nil, fmt.Errorf("creating polecats dir: %w", err)
	}

	// Get the repo base (bare repo or mayor/rig)
	repoGit, err := m.repoBase()
	if err != nil {
		return nil, fmt.Errorf("finding repo base: %w", err)
	}

	// Always create fresh branch - unique name guarantees no collision
	// git worktree add -b polecat/<name>-<timestamp> <path>
	if err := repoGit.WorktreeAdd(polecatPath, branchName); err != nil {
		return nil, fmt.Errorf("creating worktree: %w", err)
	}

	// Install polecat CLAUDE.md template (non-fatal if template missing)
	if err := m.installCLAUDETemplate(polecatPath, name); err != nil {
		fmt.Printf("Warning: could not install CLAUDE.md template: %v\n", err)
	}

	// Set up shared beads: polecat uses rig's .beads via redirect file.
	// This eliminates git sync overhead - all polecats share one database.
	if err := m.setupSharedBeads(polecatPath); err != nil {
		// Non-fatal - polecat can still work with local beads
		// Log warning but don't fail the spawn
		fmt.Printf("Warning: could not set up shared beads: %v\n", err)
	}

	// Create agent bead for ZFC compliance (self-report state).
	// State starts as "spawning" - will be updated to "working" when Claude starts.
	agentID := m.agentBeadID(name)
	_, err = m.beads.CreateAgentBead(agentID, agentID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        m.rig.Name,
		AgentState: "spawning",
	})
	if err != nil {
		// Non-fatal - log warning but continue
		fmt.Printf("Warning: could not create agent bead: %v\n", err)
	}

	// Return polecat with derived state (no issue assigned yet = idle)
	// State is derived from beads, not stored in state.json
	now := time.Now()
	polecat := &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     StateIdle, // No issue assigned yet
		ClonePath: polecatPath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}

	return polecat, nil
}

// Remove deletes a polecat worktree.
// If force is true, removes even with uncommitted changes (but not stashes/unpushed).
// Use nuclear=true to bypass ALL safety checks.
func (m *Manager) Remove(name string, force bool) error {
	return m.RemoveWithOptions(name, force, false)
}

// RemoveWithOptions deletes a polecat worktree with explicit control over safety checks.
// force=true: bypass uncommitted changes check (legacy behavior)
// nuclear=true: bypass ALL safety checks including stashes and unpushed commits
func (m *Manager) RemoveWithOptions(name string, force, nuclear bool) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	polecatPath := m.polecatDir(name)
	polecatGit := git.NewGit(polecatPath)

	// Check for uncommitted work unless bypassed
	if !nuclear {
		status, err := polecatGit.CheckUncommittedWork()
		if err == nil && !status.Clean() {
			// For backward compatibility: force only bypasses uncommitted changes, not stashes/unpushed
			if force {
				// Force mode: allow uncommitted changes but still block on stashes/unpushed
				if status.StashCount > 0 || status.UnpushedCommits > 0 {
					return &UncommittedWorkError{PolecatName: name, Status: status}
				}
			} else {
				return &UncommittedWorkError{PolecatName: name, Status: status}
			}
		}
	}

	// Get repo base to remove the worktree properly
	repoGit, err := m.repoBase()
	if err != nil {
		// Fall back to direct removal if repo base not found
		return os.RemoveAll(polecatPath)
	}

	// Try to remove as a worktree first (use force flag for worktree removal too)
	if err := repoGit.WorktreeRemove(polecatPath, force); err != nil {
		// Fall back to direct removal if worktree removal fails
		// (e.g., if this is an old-style clone, not a worktree)
		if removeErr := os.RemoveAll(polecatPath); removeErr != nil {
			return fmt.Errorf("removing polecat dir: %w", removeErr)
		}
	}

	// Prune any stale worktree entries (non-fatal: cleanup only)
	_ = repoGit.WorktreePrune()

	// Release name back to pool if it's a pooled name (non-fatal: state file update)
	m.namePool.Release(name)
	_ = m.namePool.Save()

	// Delete agent bead (non-fatal: may not exist or beads may not be available)
	agentID := m.agentBeadID(name)
	if err := m.beads.DeleteAgentBead(agentID); err != nil {
		// Only log if not "not found" - it's ok if it doesn't exist
		if !errors.Is(err, beads.ErrNotFound) {
			fmt.Printf("Warning: could not delete agent bead %s: %v\n", agentID, err)
		}
	}

	return nil
}

// AllocateName allocates a name from the name pool.
// Returns a pooled name (polecat-01 through polecat-50) if available,
// otherwise returns an overflow name (rigname-N).
func (m *Manager) AllocateName() (string, error) {
	// First reconcile pool with existing polecats to handle stale state
	m.ReconcilePool()

	name, err := m.namePool.Allocate()
	if err != nil {
		return "", err
	}

	if err := m.namePool.Save(); err != nil {
		return "", fmt.Errorf("saving pool state: %w", err)
	}

	return name, nil
}

// ReleaseName releases a name back to the pool.
// This is called when a polecat is removed.
func (m *Manager) ReleaseName(name string) {
	m.namePool.Release(name)
	_ = m.namePool.Save() // non-fatal: state file update
}

// Recreate removes an existing polecat and creates a fresh worktree.
// This ensures the polecat starts with the latest code from the base branch.
// The name is preserved (not released to pool) since we're recreating immediately.
// force controls whether to bypass uncommitted changes check.
//
// Branch naming: Each recreation gets a unique branch (polecat/<name>-<timestamp>).
// Old branches are left for garbage collection - they're never pushed to origin.
func (m *Manager) Recreate(name string, force bool) (*Polecat, error) {
	if !m.exists(name) {
		return nil, ErrPolecatNotFound
	}

	polecatPath := m.polecatDir(name)
	polecatGit := git.NewGit(polecatPath)

	// Get the repo base (bare repo or mayor/rig)
	repoGit, err := m.repoBase()
	if err != nil {
		return nil, fmt.Errorf("finding repo base: %w", err)
	}

	// Check for uncommitted work unless forced
	if !force {
		status, err := polecatGit.CheckUncommittedWork()
		if err == nil && !status.Clean() {
			return nil, &UncommittedWorkError{PolecatName: name, Status: status}
		}
	}

	// Delete old agent bead before recreation (non-fatal)
	agentID := m.agentBeadID(name)
	if err := m.beads.DeleteAgentBead(agentID); err != nil {
		if !errors.Is(err, beads.ErrNotFound) {
			fmt.Printf("Warning: could not delete old agent bead %s: %v\n", agentID, err)
		}
	}

	// Remove the worktree (use force for git worktree removal)
	if err := repoGit.WorktreeRemove(polecatPath, true); err != nil {
		// Fall back to direct removal
		if removeErr := os.RemoveAll(polecatPath); removeErr != nil {
			return nil, fmt.Errorf("removing polecat dir: %w", removeErr)
		}
	}

	// Prune stale worktree entries (non-fatal: cleanup only)
	_ = repoGit.WorktreePrune()

	// Fetch latest from origin to ensure we have fresh commits (non-fatal: may be offline)
	_ = repoGit.Fetch("origin")

	// Create fresh worktree with unique branch name
	// Old branches are left behind - they're ephemeral (never pushed to origin)
	// and will be cleaned up by garbage collection
	branchName := fmt.Sprintf("polecat/%s-%d", name, time.Now().UnixMilli())
	if err := repoGit.WorktreeAdd(polecatPath, branchName); err != nil {
		return nil, fmt.Errorf("creating fresh worktree: %w", err)
	}

	// Install polecat CLAUDE.md template (non-fatal if template missing)
	if err := m.installCLAUDETemplate(polecatPath, name); err != nil {
		fmt.Printf("Warning: could not install CLAUDE.md template: %v\n", err)
	}

	// Set up shared beads
	if err := m.setupSharedBeads(polecatPath); err != nil {
		fmt.Printf("Warning: could not set up shared beads: %v\n", err)
	}

	// Create fresh agent bead for ZFC compliance
	_, err = m.beads.CreateAgentBead(agentID, agentID, &beads.AgentFields{
		RoleType:   "polecat",
		Rig:        m.rig.Name,
		AgentState: "spawning",
	})
	if err != nil {
		fmt.Printf("Warning: could not create agent bead: %v\n", err)
	}

	// Return fresh polecat
	now := time.Now()
	return &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     StateIdle,
		ClonePath: polecatPath,
		Branch:    branchName,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// ReconcilePool syncs pool state with existing polecat directories.
// This should be called to recover from crashes or stale state.
func (m *Manager) ReconcilePool() {
	polecats, err := m.List()
	if err != nil {
		return
	}

	var names []string
	for _, p := range polecats {
		names = append(names, p.Name)
	}

	m.namePool.Reconcile(names)
	_ = m.namePool.Save() // non-fatal: state file update
}

// PoolStatus returns information about the name pool.
func (m *Manager) PoolStatus() (active int, names []string) {
	return m.namePool.ActiveCount(), m.namePool.ActiveNames()
}

// List returns all polecats in the rig.
func (m *Manager) List() ([]*Polecat, error) {
	polecatsDir := filepath.Join(m.rig.Path, "polecats")

	entries, err := os.ReadDir(polecatsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading polecats dir: %w", err)
	}

	var polecats []*Polecat
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		polecat, err := m.Get(entry.Name())
		if err != nil {
			continue // Skip invalid polecats
		}
		polecats = append(polecats, polecat)
	}

	return polecats, nil
}

// Get returns a specific polecat by name.
// State is derived from beads assignee field:
// - If an issue is assigned to this polecat and is open/in_progress: StateWorking
// - If an issue is assigned but closed: StateDone
// - If no issue assigned: StateIdle
func (m *Manager) Get(name string) (*Polecat, error) {
	if !m.exists(name) {
		return nil, ErrPolecatNotFound
	}

	return m.loadFromBeads(name)
}

// SetState updates a polecat's state.
// In the beads model, state is derived from issue status:
// - StateWorking/StateActive: issue status set to in_progress
// - StateDone/StateIdle: assignee cleared from issue
// - StateStuck: issue status set to blocked (if supported)
// If beads is not available, this is a no-op.
func (m *Manager) SetState(name string, state State) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Find the issue assigned to this polecat
	assignee := m.assigneeID(name)
	issue, err := m.beads.GetAssignedIssue(assignee)
	if err != nil {
		// If beads is not available, treat as no-op (state can't be changed)
		return nil
	}

	switch state {
	case StateWorking, StateActive:
		// Set issue to in_progress if there is one
		if issue != nil {
			status := "in_progress"
			if err := m.beads.Update(issue.ID, beads.UpdateOptions{Status: &status}); err != nil {
				return fmt.Errorf("setting issue status: %w", err)
			}
		}
	case StateDone, StateIdle:
		// Clear assignment when done/idle
		if issue != nil {
			empty := ""
			if err := m.beads.Update(issue.ID, beads.UpdateOptions{Assignee: &empty}); err != nil {
				return fmt.Errorf("clearing assignee: %w", err)
			}
		}
	case StateStuck:
		// Mark issue as blocked if supported, otherwise just note in issue
		if issue != nil {
			// For now, just keep the assignment - the issue's blocked_by would indicate stuck
			// We could add a status="blocked" here if beads supports it
		}
	}

	return nil
}

// AssignIssue assigns an issue to a polecat by setting the issue's assignee in beads.
func (m *Manager) AssignIssue(name, issue string) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Set the issue's assignee to this polecat
	assignee := m.assigneeID(name)
	status := "in_progress"
	if err := m.beads.Update(issue, beads.UpdateOptions{
		Assignee: &assignee,
		Status:   &status,
	}); err != nil {
		return fmt.Errorf("setting issue assignee: %w", err)
	}

	return nil
}

// ClearIssue removes the issue assignment from a polecat.
// In the transient model, this transitions to Done state for cleanup.
// This clears the assignee from the currently assigned issue in beads.
// If beads is not available, this is a no-op.
func (m *Manager) ClearIssue(name string) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Find the issue assigned to this polecat
	assignee := m.assigneeID(name)
	issue, err := m.beads.GetAssignedIssue(assignee)
	if err != nil {
		// If beads is not available, treat as no-op
		return nil
	}

	if issue == nil {
		// No issue assigned, nothing to clear
		return nil
	}

	// Clear the assignee from the issue
	empty := ""
	if err := m.beads.Update(issue.ID, beads.UpdateOptions{
		Assignee: &empty,
	}); err != nil {
		return fmt.Errorf("clearing issue assignee: %w", err)
	}

	return nil
}

// Wake transitions a polecat from idle to active.
// Deprecated: In the transient model, polecats start in working state.
// This method is kept for backward compatibility with existing polecats.
func (m *Manager) Wake(name string) error {
	polecat, err := m.Get(name)
	if err != nil {
		return err
	}

	// Accept both idle and done states for legacy compatibility
	if polecat.State != StateIdle && polecat.State != StateDone {
		return fmt.Errorf("polecat is not idle (state: %s)", polecat.State)
	}

	return m.SetState(name, StateWorking)
}

// Sleep transitions a polecat from active to idle.
// Deprecated: In the transient model, polecats are deleted when done.
// This method is kept for backward compatibility.
func (m *Manager) Sleep(name string) error {
	polecat, err := m.Get(name)
	if err != nil {
		return err
	}

	// Accept working state as well for legacy compatibility
	if polecat.State != StateActive && polecat.State != StateWorking {
		return fmt.Errorf("polecat is not active (state: %s)", polecat.State)
	}

	return m.SetState(name, StateDone)
}

// Finish transitions a polecat from working/done/stuck to idle and clears the issue.
// This clears the assignee from any assigned issue.
func (m *Manager) Finish(name string) error {
	polecat, err := m.Get(name)
	if err != nil {
		return err
	}

	// Only allow finishing from working-related states
	switch polecat.State {
	case StateWorking, StateDone, StateStuck:
		// OK to finish
	default:
		return fmt.Errorf("polecat is not in a finishing state (state: %s)", polecat.State)
	}

	// Clear the issue assignment
	return m.ClearIssue(name)
}

// Reset forces a polecat to idle state regardless of current state.
// This clears the assignee from any assigned issue.
func (m *Manager) Reset(name string) error {
	if !m.exists(name) {
		return ErrPolecatNotFound
	}

	// Clear the issue assignment
	return m.ClearIssue(name)
}

// loadFromBeads gets polecat info from beads assignee field.
// State is simple: issue assigned → working, no issue → idle.
// We don't interpret issue status (ZFC: Go is transport, not decision-maker).
func (m *Manager) loadFromBeads(name string) (*Polecat, error) {
	polecatPath := m.polecatDir(name)

	// Get actual branch from worktree (branches are now timestamped)
	polecatGit := git.NewGit(polecatPath)
	branchName, err := polecatGit.CurrentBranch()
	if err != nil {
		// Fall back to old format if we can't read the branch
		branchName = fmt.Sprintf("polecat/%s", name)
	}

	// Query beads for assigned issue
	assignee := m.assigneeID(name)
	issue, beadsErr := m.beads.GetAssignedIssue(assignee)
	if beadsErr != nil {
		// If beads query fails, return basic polecat info
		// This allows the system to work even if beads is not available
		return &Polecat{
			Name:      name,
			Rig:       m.rig.Name,
			State:     StateIdle,
			ClonePath: polecatPath,
			Branch:    branchName,
		}, nil
	}

	// Simple rule: has issue = working, no issue = idle
	// We don't interpret issue.Status - that's for Claude to decide
	state := StateIdle
	issueID := ""
	if issue != nil {
		issueID = issue.ID
		state = StateWorking
	}

	return &Polecat{
		Name:      name,
		Rig:       m.rig.Name,
		State:     state,
		ClonePath: polecatPath,
		Branch:    branchName,
		Issue:     issueID,
	}, nil
}

// installCLAUDETemplate copies the polecat CLAUDE.md template into the worktree.
// Template variables {{rig}} and {{name}} are substituted with actual values.
// This provides polecats with context about their role and available commands.
func (m *Manager) installCLAUDETemplate(polecatPath, name string) error {
	// Read template from mayor/rig/templates directory
	// Templates live in the mayor's clone, not at rig root
	templatePath := filepath.Join(m.rig.Path, "mayor", "rig", "templates", "polecat-CLAUDE.md")
	content, err := os.ReadFile(templatePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Template doesn't exist - warn and skip (this is a setup issue)
			fmt.Printf("Warning: polecat template not found at %s\n", templatePath)
			return nil
		}
		return fmt.Errorf("reading template: %w", err)
	}

	// Substitute template variables
	output := string(content)
	output = strings.ReplaceAll(output, "{{rig}}", m.rig.Name)
	output = strings.ReplaceAll(output, "{{name}}", name)

	// Write to polecat's CLAUDE.md
	claudePath := filepath.Join(polecatPath, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(output), 0644); err != nil {
		return fmt.Errorf("writing CLAUDE.md: %w", err)
	}

	return nil
}

// setupSharedBeads creates a redirect file so the polecat uses the rig's shared .beads database.
// This eliminates the need for git sync between polecat clones - all polecats share one database.
//
// Structure:
//
//	rig/
//	  .beads/              <- Shared database (ensured to exist)
//	  polecats/
//	    <name>/
//	      .beads/
//	        redirect       <- Contains "../../.beads"
//
// IMPORTANT: If the polecat was created from a branch that had .beads/ tracked in git,
// those files will be present. We must clean them out and replace with just the redirect.
func (m *Manager) setupSharedBeads(polecatPath string) error {
	// Ensure rig root has .beads/ directory
	rigBeadsDir := filepath.Join(m.rig.Path, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		return fmt.Errorf("creating rig .beads dir: %w", err)
	}

	// Clean up any existing .beads/ contents from the branch
	// This handles the case where the polecat was created from a branch that
	// had .beads/ tracked (e.g., from previous bd sync operations)
	polecatBeadsDir := filepath.Join(polecatPath, ".beads")
	if _, err := os.Stat(polecatBeadsDir); err == nil {
		// Directory exists - remove it entirely and recreate fresh
		if err := os.RemoveAll(polecatBeadsDir); err != nil {
			return fmt.Errorf("cleaning existing .beads dir: %w", err)
		}
	}

	// Create fresh .beads directory
	if err := os.MkdirAll(polecatBeadsDir, 0755); err != nil {
		return fmt.Errorf("creating polecat .beads dir: %w", err)
	}

	// Create redirect file pointing to mayor/rig/.beads (the canonical beads location)
	// Path is relative from polecats/<name>/.beads/ to mayor/rig/.beads/
	// We go directly to mayor/rig/.beads, not through rig root, to match crew workers
	redirectPath := filepath.Join(polecatBeadsDir, "redirect")
	redirectContent := "../../mayor/rig/.beads\n"

	if err := os.WriteFile(redirectPath, []byte(redirectContent), 0644); err != nil {
		return fmt.Errorf("creating redirect file: %w", err)
	}

	return nil
}

// CleanupStaleBranches removes orphaned polecat branches that are no longer in use.
// This includes:
// - Branches for polecats that no longer exist
// - Old timestamped branches (keeps only the most recent per polecat name)
// Returns the number of branches deleted.
func (m *Manager) CleanupStaleBranches() (int, error) {
	repoGit, err := m.repoBase()
	if err != nil {
		return 0, fmt.Errorf("finding repo base: %w", err)
	}

	// List all polecat branches
	branches, err := repoGit.ListBranches("polecat/*")
	if err != nil {
		return 0, fmt.Errorf("listing branches: %w", err)
	}

	if len(branches) == 0 {
		return 0, nil
	}

	// Get list of existing polecats
	polecats, err := m.List()
	if err != nil {
		return 0, fmt.Errorf("listing polecats: %w", err)
	}

	// Build set of current polecat branches (from actual polecat objects)
	currentBranches := make(map[string]bool)
	for _, p := range polecats {
		currentBranches[p.Branch] = true
	}

	// Delete branches not in current set
	deleted := 0
	for _, branch := range branches {
		if currentBranches[branch] {
			continue // This branch is in use
		}
		// Delete orphaned branch
		if err := repoGit.DeleteBranch(branch, true); err != nil {
			// Log but continue - non-fatal
			fmt.Printf("Warning: could not delete branch %s: %v\n", branch, err)
			continue
		}
		deleted++
	}

	return deleted, nil
}

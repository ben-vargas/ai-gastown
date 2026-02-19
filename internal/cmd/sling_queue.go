package cmd

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// EnqueueOptions holds options for enqueueing a bead.
type EnqueueOptions struct {
	Formula     string   // Formula to apply at dispatch time (e.g., "mol-polecat-work")
	Args        string   // Natural language args for executor
	Vars        []string // Formula variables (key=value)
	Merge       string   // Merge strategy: direct/mr/local
	BaseBranch  string   // Override base branch for polecat worktree
	NoConvoy    bool     // Skip auto-convoy creation
	Owned       bool     // Mark auto-convoy as caller-managed lifecycle
	DryRun      bool     // Show what would be done without acting
	Force       bool     // Force enqueue even if bead is hooked/in_progress
	NoMerge     bool     // Skip merge queue on completion
	Account     string   // Claude Code account handle
	Agent       string   // Agent override (e.g., "gemini", "codex")
	HookRawBead bool     // Hook raw bead without default formula
	Ralph       bool     // Ralph Wiggum loop mode
}

const (
	// LabelQueued marks a bead as queued for dispatch.
	LabelQueued = "gt:queued"
)

// enqueueBead queues a bead for deferred dispatch via the work queue.
// It adds labels, writes queue metadata to the description, and creates
// an auto-convoy. Does NOT spawn a polecat or hook the bead.
func enqueueBead(beadID, rigName string, opts EnqueueOptions) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	// Validate bead exists
	if err := verifyBeadExists(beadID); err != nil {
		return fmt.Errorf("bead '%s' not found", beadID)
	}

	// Validate rig exists
	if _, isRig := IsRigName(rigName); !isRig {
		return fmt.Errorf("'%s' is not a known rig", rigName)
	}

	// Cross-rig guard: prevent queuing beads to the wrong rig.
	// Polecats are worktree-scoped — a bead from Rig A dispatched in Rig B
	// creates a broken polecat. Skip when Force is set (user override).
	if !opts.Force {
		if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
			return err
		}
	}

	// Get bead info for status/label checks
	info, err := getBeadInfo(beadID)
	if err != nil {
		return fmt.Errorf("checking bead status: %w", err)
	}

	// Idempotency: skip if bead is actively queued (open + gt:queued label).
	// Dispatched beads retain gt:queued as audit trail but are hooked/closed,
	// so they should be re-queueable without --force.
	hasQueuedLabel := false
	for _, label := range info.Labels {
		if label == LabelQueued {
			hasQueuedLabel = true
			break
		}
	}
	if hasQueuedLabel && info.Status == "open" {
		fmt.Printf("%s Bead %s is already queued, no-op\n", style.Dim.Render("○"), beadID)
		return nil
	}

	// Check status: error if hooked/in_progress (unless --force)
	if (info.Status == "pinned" || info.Status == "hooked") && !opts.Force {
		return fmt.Errorf("bead %s is already %s to %s\nUse --force to override", beadID, info.Status, info.Assignee)
	}

	// Validate formula exists (lightweight check, no side effects for dry-run)
	if opts.Formula != "" {
		if err := verifyFormulaExists(opts.Formula); err != nil {
			return fmt.Errorf("formula %q not found: %w", opts.Formula, err)
		}
	}

	if opts.DryRun {
		fmt.Printf("Would queue %s → %s\n", beadID, rigName)
		fmt.Printf("  Would add label: %s\n", LabelQueued)
		fmt.Printf("  Would append queue metadata to description\n")
		if !opts.NoConvoy {
			fmt.Printf("  Would create auto-convoy\n")
		}
		return nil
	}

	// Cook formula after dry-run check to avoid side effects (bd cook writes
	// artifacts) when only previewing. Catches bad protos early before the
	// daemon tries to dispatch and silently requeues in an infinite loop.
	if opts.Formula != "" {
		workDir := beads.ResolveHookDir(townRoot, beadID, "")
		if err := CookFormula(opts.Formula, workDir, townRoot); err != nil {
			return fmt.Errorf("formula %q failed to cook: %w", opts.Formula, err)
		}
	}

	// Build queue metadata
	meta := NewQueueMetadata(rigName)
	if opts.Formula != "" {
		meta.Formula = opts.Formula
	}
	if opts.Args != "" {
		meta.Args = opts.Args
	}
	if len(opts.Vars) > 0 {
		meta.Vars = strings.Join(opts.Vars, "\n")
	}
	if opts.Merge != "" {
		meta.Merge = opts.Merge
	}
	if opts.BaseBranch != "" {
		meta.BaseBranch = opts.BaseBranch
	}
	meta.NoMerge = opts.NoMerge
	if opts.Account != "" {
		meta.Account = opts.Account
	}
	if opts.Agent != "" {
		meta.Agent = opts.Agent
	}
	meta.HookRawBead = opts.HookRawBead
	if opts.Ralph {
		meta.Mode = "ralph"
	}
	// NoBoot is intentionally NOT stored in queue metadata. Dispatch always
	// sets NoBoot=true to avoid lock contention in the daemon dispatch loop.
	// Storing it would be dead code that creates false contract signaling.
	meta.Owned = opts.Owned

	// Strip any existing queue metadata before appending new metadata.
	// This ensures idempotent re-enqueue (no duplicate ---queue--- blocks).
	baseDesc := StripQueueMetadata(info.Description)

	// Append queue metadata to bead description
	metaBlock := FormatQueueMetadata(meta)
	newDesc := baseDesc
	if newDesc != "" {
		newDesc += "\n"
	}
	newDesc += metaBlock

	// Write metadata FIRST, then add label. Metadata without the label is
	// inert (dispatch queries bd ready --label gt:queued, so unlabeled beads
	// are invisible). The label is the atomic "commit" of the enqueue.
	// This prevents a race where dispatch fires between label-add and
	// metadata-write, sees meta==nil, and irreversibly quarantines the bead.
	beadDir := resolveBeadDir(beadID)
	descCmd := exec.Command("bd", "update", beadID, "--description="+newDesc)
	descCmd.Dir = beadDir
	if err := descCmd.Run(); err != nil {
		return fmt.Errorf("writing queue metadata: %w", err)
	}

	// Add queue label (the activation signal for dispatch).
	labelCmd := exec.Command("bd", "update", beadID,
		"--add-label="+LabelQueued)
	labelCmd.Dir = beadDir
	var labelStderr bytes.Buffer
	labelCmd.Stderr = &labelStderr
	if err := labelCmd.Run(); err != nil {
		// Roll back metadata — strip it so the bead doesn't have orphaned queue data.
		rollbackCmd := exec.Command("bd", "update", beadID, "--description="+baseDesc)
		rollbackCmd.Dir = beadDir
		_ = rollbackCmd.Run() // best effort rollback
		errMsg := strings.TrimSpace(labelStderr.String())
		if errMsg != "" {
			return fmt.Errorf("adding queue label: %s", errMsg)
		}
		return fmt.Errorf("adding queue label: %w", err)
	}

	// Auto-convoy (unless --no-convoy)
	if !opts.NoConvoy {
		existingConvoy := isTrackedByConvoy(beadID)
		if existingConvoy == "" {
			convoyID, err := createAutoConvoy(beadID, info.Title, opts.Owned, opts.Merge)
			if err != nil {
				fmt.Printf("%s Could not create auto-convoy: %v\n", style.Dim.Render("Warning:"), err)
			} else {
				fmt.Printf("%s Created convoy %s\n", style.Bold.Render("→"), convoyID)
				// Re-persist metadata with convoy ID so dispatch can see it
				meta.Convoy = convoyID
				updatedBlock := FormatQueueMetadata(meta)
				updatedDesc := baseDesc
				if updatedDesc != "" {
					updatedDesc += "\n"
				}
				updatedDesc += updatedBlock
				convoyDescCmd := exec.Command("bd", "update", beadID, "--description="+updatedDesc)
				convoyDescCmd.Dir = beadDir
				if err := convoyDescCmd.Run(); err != nil {
					fmt.Printf("%s Could not update metadata with convoy: %v\n", style.Dim.Render("Warning:"), err)
				}
			}
		} else {
			fmt.Printf("%s Already tracked by convoy %s\n", style.Dim.Render("○"), existingConvoy)
		}
	}

	// Log enqueue event
	actor := detectActor()
	_ = events.LogFeed(events.TypeQueueEnqueue, actor, events.QueueEnqueuePayload(beadID, rigName))

	fmt.Printf("%s Queued %s → %s\n", style.Bold.Render("✓"), beadID, rigName)
	return nil
}

// runBatchEnqueue enqueues multiple beads for deferred dispatch.
// Called from sling when --queue is set with multiple beads and a rig target.
func runBatchEnqueue(beadIDs []string, rigName string) {
	if slingDryRun {
		fmt.Printf("%s Would queue %d beads to rig '%s':\n", style.Bold.Render("📋"), len(beadIDs), rigName)
		for _, beadID := range beadIDs {
			fmt.Printf("  Would queue: %s → %s\n", beadID, rigName)
		}
		return
	}

	fmt.Printf("%s Queuing %d beads to rig '%s'...\n", style.Bold.Render("📋"), len(beadIDs), rigName)

	successCount := 0
	for _, beadID := range beadIDs {
		// Resolve formula from flags
		formula := resolveFormula(slingFormula, slingHookRawBead)
		err := enqueueBead(beadID, rigName, EnqueueOptions{
			Formula:     formula,
			Args:        slingArgs,
			Vars:        slingVars,
			NoConvoy:    slingNoConvoy,
			Owned:       slingOwned,
			Merge:       slingMerge,
			BaseBranch:  slingBaseBranch,
			DryRun:      false,
			Force:       slingForce,
			NoMerge:     slingNoMerge,
			Account:     slingAccount,
			Agent:       slingAgent,
			HookRawBead: slingHookRawBead,
			Ralph:       slingRalph,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), beadID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Queued %d/%d beads\n", style.Bold.Render("📊"), successCount, len(beadIDs))
}

// dequeueBeadLabels removes the gt:queued label and strips queue metadata from
// a bead. If metadata stripping fails (e.g., getBeadInfo error), falls back to
// label-only removal to avoid blocking queue clear operations.
func dequeueBeadLabels(beadID string) error {
	beadDir := resolveBeadDir(beadID)

	// Try to strip queue metadata from description.
	// Best-effort: if we can't read the bead, still remove the label.
	info, err := getBeadInfo(beadID)
	if err == nil {
		stripped := StripQueueMetadata(info.Description)
		if stripped != info.Description {
			// Combine metadata strip + label removal in a single bd update
			cmd := exec.Command("bd", "update", beadID,
				"--description="+stripped,
				"--remove-label="+LabelQueued)
			cmd.Dir = beadDir
			return cmd.Run()
		}
	}

	// Fallback: label-only removal (no metadata to strip, or couldn't read bead)
	cmd := exec.Command("bd", "update", beadID, "--remove-label="+LabelQueued)
	cmd.Dir = beadDir
	return cmd.Run()
}

// resolveRigForBead determines the rig that owns a bead from its ID prefix.
// Returns empty string for town-root beads or unknown prefixes.
func resolveRigForBead(townRoot, beadID string) string {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return ""
	}
	return beads.GetRigNameForPrefix(townRoot, prefix)
}

// resolveFormula determines the formula name from user flags.
// Priority: --hook-raw-bead (suppresses all) > --formula (explicit) > default.
func resolveFormula(explicit string, hookRawBead bool) string {
	if hookRawBead {
		return ""
	}
	if explicit != "" {
		return explicit
	}
	return "mol-polecat-work"
}

// hasQueuedLabel checks if a bead has the gt:queued label.
func hasQueuedLabel(labels []string) bool {
	for _, l := range labels {
		if l == LabelQueued {
			return true
		}
	}
	return false
}

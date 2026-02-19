package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	convoyQueueDryRun  bool
	convoyQueueForce   bool
	convoyQueueFormula string
	convoyQueueHookRaw bool
)

var convoyQueueCmd = &cobra.Command{
	Use:   "queue <convoy-id>",
	Short: "Queue all open tracked issues for deferred dispatch",
	Long: `Queue all open issues tracked by a convoy for capacity-controlled dispatch.

Each issue's target rig is auto-resolved from its bead ID prefix. Town-root
beads (hq-*) are skipped since they are not dispatchable work.

ALL open issues are queued, including blocked ones. Blocked beads wait in
the queue and automatically dispatch when their blockers resolve (bd ready
filters them at dispatch time).

Issues that are already queued, closed, or assigned are skipped.

Examples:
  gt convoy queue hq-cv-abc           # Queue all open issues (auto-resolve rigs)
  gt convoy queue hq-cv-abc --dry-run # Preview what would be queued`,
	Args: cobra.ExactArgs(1),
	RunE: runConvoyQueue,
}

func init() {
	convoyQueueCmd.Flags().BoolVar(&convoyQueueDryRun, "dry-run", false, "Show what would be queued without acting")
	convoyQueueCmd.Flags().BoolVar(&convoyQueueForce, "force", false, "Force enqueue even if bead is hooked/in_progress")
	convoyQueueCmd.Flags().StringVar(&convoyQueueFormula, "formula", "", "Formula to apply (default: mol-polecat-work)")
	convoyQueueCmd.Flags().BoolVar(&convoyQueueHookRaw, "hook-raw-bead", false, "Hook raw bead without formula")

	convoyCmd.AddCommand(convoyQueueCmd)
}

func runConvoyQueue(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	// Validate convoy exists
	if err := verifyBeadExists(convoyID); err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	// Get tracked issues
	townBeads := filepath.Join(townRoot, ".beads")
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues: %w", err)
	}

	if len(tracked) == 0 {
		fmt.Printf("Convoy %s has no tracked issues.\n", convoyID)
		return nil
	}

	// Filter to queueable issues
	type queueCandidate struct {
		ID      string
		Title   string
		RigName string
	}
	var candidates []queueCandidate
	skippedClosed := 0
	skippedAssigned := 0
	skippedQueued := 0
	skippedNoRig := 0

	for _, t := range tracked {
		// Skip closed issues
		if t.Status == "closed" || t.Status == "tombstone" {
			skippedClosed++
			continue
		}

		// Skip already assigned (hooked/in_progress) unless --force
		if t.Assignee != "" && !convoyQueueForce {
			skippedAssigned++
			continue
		}

		// Check if already queued (need to get labels)
		info, err := getBeadInfo(t.ID)
		if err != nil {
			fmt.Printf("  %s Could not check %s: %v\n", style.Dim.Render("Warning:"), t.ID, err)
			continue
		}
		if hasQueuedLabel(info.Labels) {
			skippedQueued++
			continue
		}

		// Resolve rig from bead prefix
		rigName := resolveRigForBead(townRoot, t.ID)
		if rigName == "" {
			skippedNoRig++
			prefix := beads.ExtractPrefix(t.ID)
			fmt.Printf("  %s %s: cannot resolve rig from prefix %q (town-root or unknown)\n",
				style.Dim.Render("○"), t.ID, prefix)
			continue
		}

		candidates = append(candidates, queueCandidate{ID: t.ID, Title: t.Title, RigName: rigName})
	}

	if len(candidates) == 0 {
		fmt.Printf("No issues to queue from convoy %s", convoyID)
		if skippedClosed > 0 || skippedAssigned > 0 || skippedQueued > 0 || skippedNoRig > 0 {
			fmt.Printf(" (%d closed, %d assigned, %d already queued, %d no rig)",
				skippedClosed, skippedAssigned, skippedQueued, skippedNoRig)
		}
		fmt.Println()
		return nil
	}

	formula := resolveFormula(convoyQueueFormula, convoyQueueHookRaw)

	if convoyQueueDryRun {
		fmt.Printf("%s Would queue %d issue(s) from convoy %s:\n",
			style.Bold.Render("📋"), len(candidates), convoyID)
		if formula != "" {
			fmt.Printf("  Formula: %s\n", formula)
		} else {
			fmt.Printf("  Hook raw beads (no formula)\n")
		}
		for _, c := range candidates {
			fmt.Printf("  Would queue: %s → %s (%s)\n", c.ID, c.RigName, c.Title)
		}
		if skippedClosed > 0 || skippedAssigned > 0 || skippedQueued > 0 || skippedNoRig > 0 {
			fmt.Printf("\nSkipped: %d closed, %d assigned, %d already queued, %d no rig\n",
				skippedClosed, skippedAssigned, skippedQueued, skippedNoRig)
		}
		return nil
	}

	fmt.Printf("%s Queuing %d issue(s) from convoy %s...\n",
		style.Bold.Render("📋"), len(candidates), convoyID)

	successCount := 0
	for _, c := range candidates {
		err := enqueueBead(c.ID, c.RigName, EnqueueOptions{
			Formula:     formula,
			NoConvoy:    true, // Already tracked by this convoy
			Force:       convoyQueueForce,
			HookRawBead: convoyQueueHookRaw,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), c.ID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Queued %d/%d issue(s) from convoy %s\n",
		style.Bold.Render("📊"), successCount, len(candidates), convoyID)
	if skippedClosed > 0 || skippedAssigned > 0 || skippedQueued > 0 || skippedNoRig > 0 {
		fmt.Printf("  Skipped: %d closed, %d assigned, %d already queued, %d no rig\n",
			skippedClosed, skippedAssigned, skippedQueued, skippedNoRig)
	}

	return nil
}

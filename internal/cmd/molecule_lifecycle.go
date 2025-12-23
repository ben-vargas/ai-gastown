package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

func runMoleculeInstantiate(cmd *cobra.Command, args []string) error {
	molID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Try catalog first
	catalog, err := loadMoleculeCatalog(workDir)
	if err != nil {
		return fmt.Errorf("loading catalog: %w", err)
	}

	var mol *beads.Issue

	if catalogMol := catalog.Get(molID); catalogMol != nil {
		mol = catalogMol.ToIssue()
	} else {
		// Fall back to database
		mol, err = b.Show(molID)
		if err != nil {
			return fmt.Errorf("getting molecule: %w", err)
		}
	}

	if mol.Type != "molecule" {
		return fmt.Errorf("%s is not a molecule (type: %s)", molID, mol.Type)
	}

	// Validate molecule
	if err := beads.ValidateMolecule(mol); err != nil {
		return fmt.Errorf("invalid molecule: %w", err)
	}

	// Get the parent issue
	parent, err := b.Show(moleculeInstParent)
	if err != nil {
		return fmt.Errorf("getting parent issue: %w", err)
	}

	// Parse context variables
	ctx := make(map[string]string)
	for _, kv := range moleculeInstContext {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid context format %q (expected key=value)", kv)
		}
		ctx[parts[0]] = parts[1]
	}

	// Instantiate the molecule
	opts := beads.InstantiateOptions{Context: ctx}
	steps, err := b.InstantiateMolecule(mol, parent, opts)
	if err != nil {
		return fmt.Errorf("instantiating molecule: %w", err)
	}

	fmt.Printf("%s Created %d steps from %s on %s\n\n",
		style.Bold.Render("✓"), len(steps), molID, moleculeInstParent)

	for _, step := range steps {
		fmt.Printf("  %s: %s\n", style.Dim.Render(step.ID), step.Title)
	}

	return nil
}

// runMoleculeCatalog lists available molecule protos.
func runMoleculeCatalog(cmd *cobra.Command, args []string) error {
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	// Load catalog
	catalog, err := loadMoleculeCatalog(workDir)
	if err != nil {
		return fmt.Errorf("loading catalog: %w", err)
	}

	molecules := catalog.List()

	if moleculeJSON {
		type catalogEntry struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Source    string `json:"source"`
			StepCount int    `json:"step_count"`
		}

		var entries []catalogEntry
		for _, mol := range molecules {
			steps, _ := beads.ParseMoleculeSteps(mol.Description)
			entries = append(entries, catalogEntry{
				ID:        mol.ID,
				Title:     mol.Title,
				Source:    mol.Source,
				StepCount: len(steps),
			})
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	// Human-readable output
	fmt.Printf("%s Molecule Catalog (%d protos)\n\n", style.Bold.Render("🧬"), len(molecules))

	if len(molecules) == 0 {
		fmt.Printf("  %s\n", style.Dim.Render("(no protos available)"))
		return nil
	}

	for _, mol := range molecules {
		steps, _ := beads.ParseMoleculeSteps(mol.Description)
		stepCount := len(steps)

		sourceMarker := style.Dim.Render(fmt.Sprintf("[%s]", mol.Source))
		fmt.Printf("  %s: %s (%d steps) %s\n",
			style.Bold.Render(mol.ID), mol.Title, stepCount, sourceMarker)
	}

	return nil
}

// runMoleculeBurn burns (destroys) the current molecule attachment.
func runMoleculeBurn(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	if len(args) > 0 {
		target = args[0]
	} else {
		// Auto-detect from current directory
		roleCtx := detectRole(cwd, townRoot)
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity from current directory")
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find agent's pinned bead (handoff bead)
	parts := strings.Split(target, "/")
	role := parts[len(parts)-1]

	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}
	if handoff == nil {
		return fmt.Errorf("no handoff bead found for %s", target)
	}

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("%s No molecule attached to %s - nothing to burn\n",
			style.Dim.Render("ℹ"), target)
		return nil
	}

	moleculeID := attachment.AttachedMolecule

	// Detach the molecule with audit logging (this "burns" it by removing the attachment)
	_, err = b.DetachMoleculeWithAudit(handoff.ID, beads.DetachOptions{
		Operation: "burn",
		Agent:     target,
		Reason:    "molecule burned by agent",
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}

	if moleculeJSON {
		result := map[string]interface{}{
			"burned":     moleculeID,
			"from":       target,
			"handoff_id": handoff.ID,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	fmt.Printf("%s Burned molecule %s from %s\n",
		style.Bold.Render("🔥"), moleculeID, target)

	return nil
}

// runMoleculeSquash squashes the current molecule into a digest.
func runMoleculeSquash(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	if len(args) > 0 {
		target = args[0]
	} else {
		// Auto-detect from current directory
		roleCtx := detectRole(cwd, townRoot)
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity from current directory")
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Find agent's pinned bead (handoff bead)
	parts := strings.Split(target, "/")
	role := parts[len(parts)-1]

	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}
	if handoff == nil {
		return fmt.Errorf("no handoff bead found for %s", target)
	}

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		fmt.Printf("%s No molecule attached to %s - nothing to squash\n",
			style.Dim.Render("ℹ"), target)
		return nil
	}

	moleculeID := attachment.AttachedMolecule

	// Get progress info for the digest
	progress, _ := getMoleculeProgressInfo(b, moleculeID)

	// Create a digest issue
	digestTitle := fmt.Sprintf("Digest: %s", moleculeID)
	digestDesc := fmt.Sprintf(`Squashed molecule execution.

molecule: %s
agent: %s
squashed_at: %s
`, moleculeID, target, time.Now().UTC().Format(time.RFC3339))

	if progress != nil {
		digestDesc += fmt.Sprintf(`
## Execution Summary
- Steps: %d/%d completed
- Status: %s
`, progress.DoneSteps, progress.TotalSteps, func() string {
			if progress.Complete {
				return "complete"
			}
			return "partial"
		}())
	}

	// Create the digest bead
	digestIssue, err := b.Create(beads.CreateOptions{
		Title:       digestTitle,
		Description: digestDesc,
		Type:        "task",
		Priority:    4, // P4 - backlog priority for digests
	})
	if err != nil {
		return fmt.Errorf("creating digest: %w", err)
	}

	// Add the digest label
	_ = b.Update(digestIssue.ID, beads.UpdateOptions{
		AddLabels: []string{"digest"},
	})

	// Close the digest immediately
	closedStatus := "closed"
	err = b.Update(digestIssue.ID, beads.UpdateOptions{
		Status: &closedStatus,
	})
	if err != nil {
		fmt.Printf("%s Created digest but couldn't close it: %v\n",
			style.Dim.Render("Warning:"), err)
	}

	// Detach the molecule from the handoff bead with audit logging
	_, err = b.DetachMoleculeWithAudit(handoff.ID, beads.DetachOptions{
		Operation: "squash",
		Agent:     target,
		Reason:    fmt.Sprintf("molecule squashed to digest %s", digestIssue.ID),
	})
	if err != nil {
		return fmt.Errorf("detaching molecule: %w", err)
	}

	if moleculeJSON {
		result := map[string]interface{}{
			"squashed":   moleculeID,
			"digest_id":  digestIssue.ID,
			"from":       target,
			"handoff_id": handoff.ID,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	fmt.Printf("%s Squashed molecule %s → digest %s\n",
		style.Bold.Render("📦"), moleculeID, digestIssue.ID)

	return nil
}

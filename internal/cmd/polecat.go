package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Polecat command flags
var (
	polecatListJSON bool
	polecatListAll  bool
	polecatForce    bool
)

var polecatCmd = &cobra.Command{
	Use:   "polecat",
	Short: "Manage polecats in rigs",
	Long: `Manage polecat lifecycle in rigs.

Polecats are worker agents that operate in their own git clones.
Use the subcommands to add, remove, list, wake, and sleep polecats.`,
}

var polecatListCmd = &cobra.Command{
	Use:   "list [rig]",
	Short: "List polecats in a rig",
	Long: `List polecats in a rig or all rigs.

Output:
  - Name
  - State (idle/active/working/done/stuck)
  - Current issue (if any)
  - Session status (running/stopped)

Examples:
  gt polecat list gastown
  gt polecat list --all
  gt polecat list gastown --json`,
	RunE: runPolecatList,
}

var polecatAddCmd = &cobra.Command{
	Use:   "add <rig> <name>",
	Short: "Add a new polecat to a rig",
	Long: `Add a new polecat to a rig.

Creates a polecat directory, clones the rig repo, creates a work branch,
and initializes state.

Example:
  gt polecat add gastown Toast`,
	Args: cobra.ExactArgs(2),
	RunE: runPolecatAdd,
}

var polecatRemoveCmd = &cobra.Command{
	Use:   "remove <rig>/<polecat>",
	Short: "Remove a polecat from a rig",
	Long: `Remove a polecat from a rig.

Fails if session is running (stop first).
Warns if uncommitted changes exist.
Use --force to bypass checks.

Example:
  gt polecat remove gastown/Toast
  gt polecat remove gastown/Toast --force`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatRemove,
}

var polecatWakeCmd = &cobra.Command{
	Use:   "wake <rig>/<polecat>",
	Short: "Mark polecat as active (ready for work)",
	Long: `Mark polecat as active (ready for work).

Transitions: idle → active

Example:
  gt polecat wake gastown/Toast`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatWake,
}

var polecatSleepCmd = &cobra.Command{
	Use:   "sleep <rig>/<polecat>",
	Short: "Mark polecat as idle (not available)",
	Long: `Mark polecat as idle (not available).

Transitions: active → idle
Fails if session is running (stop first).

Example:
  gt polecat sleep gastown/Toast`,
	Args: cobra.ExactArgs(1),
	RunE: runPolecatSleep,
}

func init() {
	// List flags
	polecatListCmd.Flags().BoolVar(&polecatListJSON, "json", false, "Output as JSON")
	polecatListCmd.Flags().BoolVar(&polecatListAll, "all", false, "List polecats in all rigs")

	// Remove flags
	polecatRemoveCmd.Flags().BoolVarP(&polecatForce, "force", "f", false, "Force removal, bypassing checks")

	// Add subcommands
	polecatCmd.AddCommand(polecatListCmd)
	polecatCmd.AddCommand(polecatAddCmd)
	polecatCmd.AddCommand(polecatRemoveCmd)
	polecatCmd.AddCommand(polecatWakeCmd)
	polecatCmd.AddCommand(polecatSleepCmd)

	rootCmd.AddCommand(polecatCmd)
}

// PolecatListItem represents a polecat in list output.
type PolecatListItem struct {
	Rig            string        `json:"rig"`
	Name           string        `json:"name"`
	State          polecat.State `json:"state"`
	Issue          string        `json:"issue,omitempty"`
	SessionRunning bool          `json:"session_running"`
}

// getPolecatManager creates a polecat manager for the given rig.
func getPolecatManager(rigName string) (*polecat.Manager, *rig.Rig, error) {
	// Find town root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil, nil, fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsConfigPath := filepath.Join(townRoot, "config", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	// Get rig
	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return nil, nil, fmt.Errorf("rig '%s' not found", rigName)
	}

	// Create polecat manager
	polecatGit := git.NewGit(r.Path)
	mgr := polecat.NewManager(r, polecatGit)

	return mgr, r, nil
}

func runPolecatList(cmd *cobra.Command, args []string) error {
	var rigs []*rig.Rig

	if polecatListAll {
		// List all rigs
		allRigs, _, err := getAllRigs()
		if err != nil {
			return err
		}
		rigs = allRigs
	} else {
		// Need a rig name
		if len(args) < 1 {
			return fmt.Errorf("rig name required (or use --all)")
		}
		_, r, err := getPolecatManager(args[0])
		if err != nil {
			return err
		}
		rigs = []*rig.Rig{r}
	}

	// Collect polecats from all rigs
	t := tmux.NewTmux()
	var allPolecats []PolecatListItem

	for _, r := range rigs {
		polecatGit := git.NewGit(r.Path)
		mgr := polecat.NewManager(r, polecatGit)
		sessMgr := session.NewManager(t, r)

		polecats, err := mgr.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to list polecats in %s: %v\n", r.Name, err)
			continue
		}

		for _, p := range polecats {
			running, _ := sessMgr.IsRunning(p.Name)
			allPolecats = append(allPolecats, PolecatListItem{
				Rig:            r.Name,
				Name:           p.Name,
				State:          p.State,
				Issue:          p.Issue,
				SessionRunning: running,
			})
		}
	}

	// Output
	if polecatListJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(allPolecats)
	}

	if len(allPolecats) == 0 {
		fmt.Println("No polecats found.")
		return nil
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Polecats"))
	for _, p := range allPolecats {
		// Session indicator
		sessionStatus := style.Dim.Render("○")
		if p.SessionRunning {
			sessionStatus = style.Success.Render("●")
		}

		// State color
		stateStr := string(p.State)
		switch p.State {
		case polecat.StateWorking:
			stateStr = style.Info.Render(stateStr)
		case polecat.StateStuck:
			stateStr = style.Warning.Render(stateStr)
		case polecat.StateDone:
			stateStr = style.Success.Render(stateStr)
		default:
			stateStr = style.Dim.Render(stateStr)
		}

		fmt.Printf("  %s %s/%s  %s\n", sessionStatus, p.Rig, p.Name, stateStr)
		if p.Issue != "" {
			fmt.Printf("    %s\n", style.Dim.Render(p.Issue))
		}
	}

	return nil
}

func runPolecatAdd(cmd *cobra.Command, args []string) error {
	rigName := args[0]
	polecatName := args[1]

	mgr, _, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	fmt.Printf("Adding polecat %s to rig %s...\n", polecatName, rigName)

	p, err := mgr.Add(polecatName)
	if err != nil {
		return fmt.Errorf("adding polecat: %w", err)
	}

	fmt.Printf("%s Polecat %s added.\n", style.SuccessPrefix, p.Name)
	fmt.Printf("  %s\n", style.Dim.Render(p.ClonePath))
	fmt.Printf("  Branch: %s\n", style.Dim.Render(p.Branch))

	return nil
}

func runPolecatRemove(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Check if session is running
	if !polecatForce {
		t := tmux.NewTmux()
		sessMgr := session.NewManager(t, r)
		running, _ := sessMgr.IsRunning(polecatName)
		if running {
			return fmt.Errorf("session is running. Stop it first with: gt session stop %s/%s", rigName, polecatName)
		}
	}

	fmt.Printf("Removing polecat %s/%s...\n", rigName, polecatName)

	if err := mgr.Remove(polecatName); err != nil {
		if errors.Is(err, polecat.ErrHasChanges) && !polecatForce {
			return fmt.Errorf("polecat has uncommitted changes. Use --force to remove anyway")
		}
		return fmt.Errorf("removing polecat: %w", err)
	}

	fmt.Printf("%s Polecat %s removed.\n", style.SuccessPrefix, polecatName)
	return nil
}

func runPolecatWake(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, _, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	if err := mgr.Wake(polecatName); err != nil {
		return fmt.Errorf("waking polecat: %w", err)
	}

	fmt.Printf("%s Polecat %s is now active.\n", style.SuccessPrefix, polecatName)
	return nil
}

func runPolecatSleep(cmd *cobra.Command, args []string) error {
	rigName, polecatName, err := parseAddress(args[0])
	if err != nil {
		return err
	}

	mgr, r, err := getPolecatManager(rigName)
	if err != nil {
		return err
	}

	// Check if session is running
	t := tmux.NewTmux()
	sessMgr := session.NewManager(t, r)
	running, _ := sessMgr.IsRunning(polecatName)
	if running {
		return fmt.Errorf("session is running. Stop it first with: gt session stop %s/%s", rigName, polecatName)
	}

	if err := mgr.Sleep(polecatName); err != nil {
		return fmt.Errorf("sleeping polecat: %w", err)
	}

	fmt.Printf("%s Polecat %s is now idle.\n", style.SuccessPrefix, polecatName)
	return nil
}

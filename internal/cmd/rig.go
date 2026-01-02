// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/witness"
	"github.com/steveyegge/gastown/internal/workspace"
)

var rigCmd = &cobra.Command{
	Use:     "rig",
	GroupID: GroupWorkspace,
	Short:   "Manage rigs in the workspace",
	RunE:    requireSubcommand,
	Long: `Manage rigs (project containers) in the Gas Town workspace.

A rig is a container for managing a project and its agents:
  - refinery/rig/  Canonical main clone (Refinery's working copy)
  - mayor/rig/     Mayor's working clone for this rig
  - crew/<name>/   Human workspace(s)
  - witness/       Witness agent (no clone)
  - polecats/      Worker directories
  - .beads/        Rig-level issue tracking`,
}

var rigAddCmd = &cobra.Command{
	Use:   "add <name> <git-url>",
	Short: "Add a new rig to the workspace",
	Long: `Add a new rig by cloning a repository.

This creates a rig container with:
  - config.json           Rig configuration
  - .beads/               Rig-level issue tracking (initialized)
  - plugins/              Rig-level plugin directory
  - refinery/rig/         Canonical main clone
  - mayor/rig/            Mayor's working clone
  - crew/                 Empty crew directory (add members with 'gt crew add')
  - witness/              Witness agent directory
  - polecats/             Worker directory (empty)

The command also:
  - Seeds patrol molecules (Deacon, Witness, Refinery)
  - Creates ~/gt/plugins/ (town-level) if it doesn't exist
  - Creates <rig>/plugins/ (rig-level)

Example:
  gt rig add gastown https://github.com/steveyegge/gastown
  gt rig add my-project git@github.com:user/repo.git --prefix mp`,
	Args: cobra.ExactArgs(2),
	RunE: runRigAdd,
}

var rigListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all rigs in the workspace",
	RunE:  runRigList,
}

var rigRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a rig from the registry (does not delete files)",
	Args:  cobra.ExactArgs(1),
	RunE:  runRigRemove,
}

var rigResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset rig state (handoff content, mail, stale issues)",
	Long: `Reset various rig state.

By default, resets all resettable state. Use flags to reset specific items.

Examples:
  gt rig reset              # Reset all state
  gt rig reset --handoff    # Clear handoff content only
  gt rig reset --mail       # Clear stale mail messages only
  gt rig reset --stale      # Reset orphaned in_progress issues
  gt rig reset --stale --dry-run  # Preview what would be reset`,
	RunE: runRigReset,
}

var rigBootCmd = &cobra.Command{
	Use:   "boot <rig>",
	Short: "Start witness and refinery for a rig",
	Long: `Start the witness and refinery agents for a rig.

This is the inverse of 'gt rig shutdown'. It starts:
- The witness (if not already running)
- The refinery (if not already running)

Polecats are NOT started by this command - they are spawned
on demand when work is assigned.

Examples:
  gt rig boot greenplace`,
	Args: cobra.ExactArgs(1),
	RunE: runRigBoot,
}

var rigRebootCmd = &cobra.Command{
	Use:   "reboot <rig>",
	Short: "Restart witness and refinery for a rig",
	Long: `Restart the patrol agents (witness and refinery) for a rig.

This is equivalent to 'gt rig shutdown' followed by 'gt rig boot'.
Useful after polecats complete work and land their changes.

Examples:
  gt rig reboot greenplace
  gt rig reboot beads --force`,
	Args: cobra.ExactArgs(1),
	RunE: runRigReboot,
}

var rigShutdownCmd = &cobra.Command{
	Use:   "shutdown <rig>",
	Short: "Gracefully stop all rig agents",
	Long: `Stop all agents in a rig.

This command gracefully shuts down:
- All polecat sessions
- The refinery (if running)
- The witness (if running)

Before shutdown, checks all polecats for uncommitted work:
- Uncommitted changes (modified/untracked files)
- Stashes
- Unpushed commits

Use --force to skip graceful shutdown and kill immediately.
Use --nuclear to bypass ALL safety checks (will lose work!).

Examples:
  gt rig shutdown greenplace
  gt rig shutdown greenplace --force
  gt rig shutdown greenplace --nuclear  # DANGER: loses uncommitted work`,
	Args: cobra.ExactArgs(1),
	RunE: runRigShutdown,
}

// Flags
var (
	rigAddPrefix       string
	rigResetHandoff    bool
	rigResetMail       bool
	rigResetStale      bool
	rigResetDryRun     bool
	rigResetRole       string
	rigShutdownForce   bool
	rigShutdownNuclear bool
)

func init() {
	rootCmd.AddCommand(rigCmd)
	rigCmd.AddCommand(rigAddCmd)
	rigCmd.AddCommand(rigBootCmd)
	rigCmd.AddCommand(rigListCmd)
	rigCmd.AddCommand(rigRebootCmd)
	rigCmd.AddCommand(rigRemoveCmd)
	rigCmd.AddCommand(rigResetCmd)
	rigCmd.AddCommand(rigShutdownCmd)

	rigAddCmd.Flags().StringVar(&rigAddPrefix, "prefix", "", "Beads issue prefix (default: derived from name)")

	rigResetCmd.Flags().BoolVar(&rigResetHandoff, "handoff", false, "Clear handoff content")
	rigResetCmd.Flags().BoolVar(&rigResetMail, "mail", false, "Clear stale mail messages")
	rigResetCmd.Flags().BoolVar(&rigResetStale, "stale", false, "Reset orphaned in_progress issues (no active session)")
	rigResetCmd.Flags().BoolVar(&rigResetDryRun, "dry-run", false, "Show what would be reset without making changes")
	rigResetCmd.Flags().StringVar(&rigResetRole, "role", "", "Role to reset (default: auto-detect from cwd)")

	rigShutdownCmd.Flags().BoolVarP(&rigShutdownForce, "force", "f", false, "Force immediate shutdown")
	rigShutdownCmd.Flags().BoolVar(&rigShutdownNuclear, "nuclear", false, "DANGER: Bypass ALL safety checks (loses uncommitted work!)")

	rigRebootCmd.Flags().BoolVarP(&rigShutdownForce, "force", "f", false, "Force immediate shutdown during reboot")
}

func runRigAdd(cmd *cobra.Command, args []string) error {
	name := args[0]
	gitURL := args[1]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		// Create new if doesn't exist
		rigsConfig = &config.RigsConfig{
			Version: 1,
			Rigs:    make(map[string]config.RigEntry),
		}
	}

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	fmt.Printf("Creating rig %s...\n", style.Bold.Render(name))
	fmt.Printf("  Repository: %s\n", gitURL)

	startTime := time.Now()

	// Add the rig
	newRig, err := mgr.AddRig(rig.AddRigOptions{
		Name:        name,
		GitURL:      gitURL,
		BeadsPrefix: rigAddPrefix,
	})
	if err != nil {
		return fmt.Errorf("adding rig: %w", err)
	}

	// Save updated rigs config
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("saving rigs config: %w", err)
	}

	// Add route to town-level routes.jsonl for prefix-based routing
	if newRig.Config.Prefix != "" {
		route := beads.Route{
			Prefix: newRig.Config.Prefix + "-",
			Path:   name + "/mayor/rig",
		}
		if err := beads.AppendRoute(townRoot, route); err != nil {
			// Non-fatal: routing will still work, just not from town root
			fmt.Printf("  %s Could not update routes.jsonl: %v\n", style.Warning.Render("!"), err)
		}
	}

	elapsed := time.Since(startTime)

	// Read default branch from rig config
	defaultBranch := "main"
	if rigCfg, err := rig.LoadRigConfig(filepath.Join(townRoot, name)); err == nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}

	fmt.Printf("\n%s Rig created in %.1fs\n", style.Success.Render("✓"), elapsed.Seconds())
	fmt.Printf("\nStructure:\n")
	fmt.Printf("  %s/\n", name)
	fmt.Printf("  ├── config.json\n")
	fmt.Printf("  ├── .repo.git/        (shared bare repo for refinery+polecats)\n")
	fmt.Printf("  ├── .beads/           (prefix: %s)\n", newRig.Config.Prefix)
	fmt.Printf("  ├── plugins/          (rig-level plugins)\n")
	fmt.Printf("  ├── mayor/rig/        (clone: %s)\n", defaultBranch)
	fmt.Printf("  ├── refinery/rig/     (worktree: %s, sees polecat branches)\n", defaultBranch)
	fmt.Printf("  ├── crew/             (empty - add crew with 'gt crew add')\n")
	fmt.Printf("  ├── witness/\n")
	fmt.Printf("  └── polecats/\n")

	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  gt crew add <name> --rig %s   # Create your workspace\n", name)
	fmt.Printf("  cd %s/crew/<name>       # Work in your clone\n", filepath.Join(townRoot, name))

	return nil
}

func runRigList(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		fmt.Println("No rigs configured.")
		return nil
	}

	if len(rigsConfig.Rigs) == 0 {
		fmt.Println("No rigs configured.")
		fmt.Printf("\nAdd one with: %s\n", style.Dim.Render("gt rig add <name> <git-url>"))
		return nil
	}

	// Create rig manager to get details
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	fmt.Printf("Rigs in %s:\n\n", townRoot)

	for name := range rigsConfig.Rigs {
		r, err := mgr.GetRig(name)
		if err != nil {
			fmt.Printf("  %s %s\n", style.Warning.Render("!"), name)
			continue
		}

		summary := r.Summary()
		fmt.Printf("  %s\n", style.Bold.Render(name))
		fmt.Printf("    Polecats: %d  Crew: %d\n", summary.PolecatCount, summary.CrewCount)

		agents := []string{}
		if summary.HasRefinery {
			agents = append(agents, "refinery")
		}
		if summary.HasWitness {
			agents = append(agents, "witness")
		}
		if r.HasMayor {
			agents = append(agents, "mayor")
		}
		if len(agents) > 0 {
			fmt.Printf("    Agents: %v\n", agents)
		}
		fmt.Println()
	}

	return nil
}

func runRigRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return fmt.Errorf("loading rigs config: %w", err)
	}

	// Create rig manager
	g := git.NewGit(townRoot)
	mgr := rig.NewManager(townRoot, rigsConfig, g)

	if err := mgr.RemoveRig(name); err != nil {
		return fmt.Errorf("removing rig: %w", err)
	}

	// Save updated config
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("saving rigs config: %w", err)
	}

	fmt.Printf("%s Rig %s removed from registry\n", style.Success.Render("✓"), name)
	fmt.Printf("\nNote: Files at %s were NOT deleted.\n", filepath.Join(townRoot, name))
	fmt.Printf("To delete: %s\n", style.Dim.Render(fmt.Sprintf("rm -rf %s", filepath.Join(townRoot, name))))

	return nil
}

func runRigReset(cmd *cobra.Command, args []string) error {
	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Determine role to reset
	roleKey := rigResetRole
	if roleKey == "" {
		// Auto-detect using env-aware role detection
		roleInfo, err := GetRoleWithContext(cwd, townRoot)
		if err != nil {
			return fmt.Errorf("detecting role: %w", err)
		}
		if roleInfo.Role == RoleUnknown {
			return fmt.Errorf("could not detect role; use --role to specify")
		}
		roleKey = string(roleInfo.Role)
	}

	// If no specific flags, reset all; otherwise only reset what's specified
	resetAll := !rigResetHandoff && !rigResetMail && !rigResetStale

	// Town beads for handoff/mail operations
	townBd := beads.New(townRoot)
	// Rig beads for issue operations (uses cwd to find .beads/)
	rigBd := beads.New(cwd)

	// Reset handoff content
	if resetAll || rigResetHandoff {
		if err := townBd.ClearHandoffContent(roleKey); err != nil {
			return fmt.Errorf("clearing handoff content: %w", err)
		}
		fmt.Printf("%s Cleared handoff content for %s\n", style.Success.Render("✓"), roleKey)
	}

	// Clear stale mail messages
	if resetAll || rigResetMail {
		result, err := townBd.ClearMail("Cleared during reset")
		if err != nil {
			return fmt.Errorf("clearing mail: %w", err)
		}
		if result.Closed > 0 || result.Cleared > 0 {
			fmt.Printf("%s Cleared mail: %d closed, %d pinned cleared\n",
				style.Success.Render("✓"), result.Closed, result.Cleared)
		} else {
			fmt.Printf("%s No mail to clear\n", style.Success.Render("✓"))
		}
	}

	// Reset stale in_progress issues
	if resetAll || rigResetStale {
		if err := runResetStale(rigBd, rigResetDryRun); err != nil {
			return fmt.Errorf("resetting stale issues: %w", err)
		}
	}

	return nil
}

// runResetStale resets in_progress issues whose assigned agent no longer has a session.
func runResetStale(bd *beads.Beads, dryRun bool) error {
	t := tmux.NewTmux()

	// Get all in_progress issues
	issues, err := bd.List(beads.ListOptions{
		Status:   "in_progress",
		Priority: -1, // All priorities
	})
	if err != nil {
		return fmt.Errorf("listing in_progress issues: %w", err)
	}

	if len(issues) == 0 {
		fmt.Printf("%s No in_progress issues found\n", style.Success.Render("✓"))
		return nil
	}

	var resetCount, skippedCount int
	var resetIssues []string

	for _, issue := range issues {
		if issue.Assignee == "" {
			continue // No assignee to check
		}

		// Parse assignee: rig/name or rig/crew/name
		sessionName, isPersistent := assigneeToSessionName(issue.Assignee)
		if sessionName == "" {
			continue // Couldn't parse assignee
		}

		// Check if session exists
		hasSession, err := t.HasSession(sessionName)
		if err != nil {
			// tmux error, skip this one
			continue
		}

		if hasSession {
			continue // Session exists, not stale
		}

		// For crew (persistent identities), only reset if explicitly checking sessions
		if isPersistent {
			skippedCount++
			if dryRun {
				fmt.Printf("  %s: %s %s\n",
					style.Dim.Render(issue.ID),
					issue.Assignee,
					style.Dim.Render("(persistent, skipped)"))
			}
			continue
		}

		// Session doesn't exist - this is stale
		if dryRun {
			fmt.Printf("  %s: %s (no session) → open\n",
				style.Bold.Render(issue.ID),
				issue.Assignee)
		} else {
			// Reset status to open and clear assignee
			openStatus := "open"
			emptyAssignee := ""
			if err := bd.Update(issue.ID, beads.UpdateOptions{
				Status:   &openStatus,
				Assignee: &emptyAssignee,
			}); err != nil {
				fmt.Printf("  %s Failed to reset %s: %v\n",
					style.Warning.Render("⚠"),
					issue.ID, err)
				continue
			}
		}
		resetCount++
		resetIssues = append(resetIssues, issue.ID)
	}

	if dryRun {
		if resetCount > 0 || skippedCount > 0 {
			fmt.Printf("\n%s Would reset %d issues, skip %d persistent\n",
				style.Dim.Render("(dry-run)"),
				resetCount, skippedCount)
		} else {
			fmt.Printf("%s No stale issues found\n", style.Success.Render("✓"))
		}
	} else {
		if resetCount > 0 {
			fmt.Printf("%s Reset %d stale issues: %v\n",
				style.Success.Render("✓"),
				resetCount, resetIssues)
		} else {
			fmt.Printf("%s No stale issues to reset\n", style.Success.Render("✓"))
		}
		if skippedCount > 0 {
			fmt.Printf("  Skipped %d persistent (crew) issues\n", skippedCount)
		}
	}

	return nil
}

// assigneeToSessionName converts an assignee (rig/name or rig/crew/name) to tmux session name.
// Returns the session name and whether this is a persistent identity (crew).
func assigneeToSessionName(assignee string) (sessionName string, isPersistent bool) {
	parts := strings.Split(assignee, "/")

	switch len(parts) {
	case 2:
		// rig/polecatName -> gt-rig-polecatName
		return fmt.Sprintf("gt-%s-%s", parts[0], parts[1]), false
	case 3:
		// rig/crew/name -> gt-rig-crew-name
		if parts[1] == "crew" {
			return fmt.Sprintf("gt-%s-crew-%s", parts[0], parts[2]), true
		}
		// Other 3-part formats not recognized
		return "", false
	default:
		return "", false
	}
}

// Helper to check if path exists
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runRigBoot(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config and get rig
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	fmt.Printf("Booting rig %s...\n", style.Bold.Render(rigName))

	var started []string
	var skipped []string

	t := tmux.NewTmux()

	// 1. Start the witness
	// Check actual tmux session, not state file (may be stale)
	witnessSession := fmt.Sprintf("gt-%s-witness", rigName)
	witnessRunning, _ := t.HasSession(witnessSession)
	if witnessRunning {
		skipped = append(skipped, "witness (already running)")
	} else {
		fmt.Printf("  Starting witness...\n")
		// Use ensureWitnessSession to create tmux session (same as gt witness start)
		created, err := ensureWitnessSession(rigName, r)
		if err != nil {
			return fmt.Errorf("starting witness: %w", err)
		}
		if created {
			// Update manager state to reflect running session
			witMgr := witness.NewManager(r)
			_ = witMgr.Start() // non-fatal: state file update
			started = append(started, "witness")
		}
	}

	// 2. Start the refinery
	// Check actual tmux session, not state file (may be stale)
	refinerySession := fmt.Sprintf("gt-%s-refinery", rigName)
	refineryRunning, _ := t.HasSession(refinerySession)
	if refineryRunning {
		skipped = append(skipped, "refinery (already running)")
	} else {
		fmt.Printf("  Starting refinery...\n")
		refMgr := refinery.NewManager(r)
		if err := refMgr.Start(false); err != nil { // false = background mode
			return fmt.Errorf("starting refinery: %w", err)
		}
		started = append(started, "refinery")
	}

	// Report results
	if len(started) > 0 {
		fmt.Printf("%s Started: %s\n", style.Success.Render("✓"), strings.Join(started, ", "))
	}
	if len(skipped) > 0 {
		fmt.Printf("%s Skipped: %s\n", style.Dim.Render("•"), strings.Join(skipped, ", "))
	}

	return nil
}

func runRigShutdown(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	// Find workspace
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Load rigs config and get rig
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		rigsConfig = &config.RigsConfig{Rigs: make(map[string]config.RigEntry)}
	}

	g := git.NewGit(townRoot)
	rigMgr := rig.NewManager(townRoot, rigsConfig, g)
	r, err := rigMgr.GetRig(rigName)
	if err != nil {
		return fmt.Errorf("rig '%s' not found", rigName)
	}

	// Check all polecats for uncommitted work (unless nuclear)
	if !rigShutdownNuclear {
		polecatGit := git.NewGit(r.Path)
		polecatMgr := polecat.NewManager(r, polecatGit)
		polecats, err := polecatMgr.List()
		if err == nil && len(polecats) > 0 {
			var problemPolecats []struct {
				name   string
				status *git.UncommittedWorkStatus
			}

			for _, p := range polecats {
				pGit := git.NewGit(p.ClonePath)
				status, err := pGit.CheckUncommittedWork()
				if err == nil && !status.Clean() {
					problemPolecats = append(problemPolecats, struct {
						name   string
						status *git.UncommittedWorkStatus
					}{p.Name, status})
				}
			}

			if len(problemPolecats) > 0 {
				fmt.Printf("\n%s Cannot shutdown - polecats have uncommitted work:\n\n", style.Warning.Render("⚠"))
				for _, pp := range problemPolecats {
					fmt.Printf("  %s: %s\n", style.Bold.Render(pp.name), pp.status.String())
				}
				fmt.Printf("\nUse %s to force shutdown (DANGER: will lose work!)\n", style.Bold.Render("--nuclear"))
				return fmt.Errorf("refusing to shutdown with uncommitted work")
			}
		}
	}

	fmt.Printf("Shutting down rig %s...\n", style.Bold.Render(rigName))

	var errors []string

	// 1. Stop all polecat sessions
	t := tmux.NewTmux()
	sessMgr := session.NewManager(t, r)
	infos, err := sessMgr.List()
	if err == nil && len(infos) > 0 {
		fmt.Printf("  Stopping %d polecat session(s)...\n", len(infos))
		if err := sessMgr.StopAll(rigShutdownForce); err != nil {
			errors = append(errors, fmt.Sprintf("polecat sessions: %v", err))
		}
	}

	// 2. Stop the refinery
	refMgr := refinery.NewManager(r)
	refStatus, err := refMgr.Status()
	if err == nil && refStatus.State == refinery.StateRunning {
		fmt.Printf("  Stopping refinery...\n")
		if err := refMgr.Stop(); err != nil {
			errors = append(errors, fmt.Sprintf("refinery: %v", err))
		}
	}

	// 3. Stop the witness
	witMgr := witness.NewManager(r)
	witStatus, err := witMgr.Status()
	if err == nil && witStatus.State == witness.StateRunning {
		fmt.Printf("  Stopping witness...\n")
		if err := witMgr.Stop(); err != nil {
			errors = append(errors, fmt.Sprintf("witness: %v", err))
		}
	}

	if len(errors) > 0 {
		fmt.Printf("\n%s Some agents failed to stop:\n", style.Warning.Render("⚠"))
		for _, e := range errors {
			fmt.Printf("  - %s\n", e)
		}
		return fmt.Errorf("shutdown incomplete")
	}

	fmt.Printf("%s Rig %s shut down successfully\n", style.Success.Render("✓"), rigName)
	return nil
}

func runRigReboot(cmd *cobra.Command, args []string) error {
	rigName := args[0]

	fmt.Printf("Rebooting rig %s...\n\n", style.Bold.Render(rigName))

	// Shutdown first
	if err := runRigShutdown(cmd, args); err != nil {
		// If shutdown fails due to uncommitted work, propagate the error
		return err
	}

	fmt.Println() // Blank line between shutdown and boot

	// Boot
	if err := runRigBoot(cmd, args); err != nil {
		return fmt.Errorf("boot failed: %w", err)
	}

	fmt.Printf("\n%s Rig %s rebooted successfully\n", style.Success.Render("✓"), rigName)
	return nil
}

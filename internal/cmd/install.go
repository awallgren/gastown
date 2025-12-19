package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/templates"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	installForce    bool
	installName     string
	installNoBeads  bool
	installGit      bool
	installGitHub   string
	installPrivate  bool
)

var installCmd = &cobra.Command{
	Use:   "install [path]",
	Short: "Create a new Gas Town harness (workspace)",
	Long: `Create a new Gas Town harness at the specified path.

A harness is the top-level directory where Gas Town is installed. It contains:
  - CLAUDE.md            Mayor role context (Mayor runs from harness root)
  - mayor/               Mayor config and state
  - rigs/                Managed rig clones (created by 'gt rig add')
  - .beads/              Town-level beads DB (mayor mail, coordination)

If path is omitted, uses the current directory.

Examples:
  gt install ~/gt                         # Create harness at ~/gt
  gt install . --name my-workspace        # Initialize current dir
  gt install ~/gt --no-beads              # Skip .beads/ initialization
  gt install ~/gt --git                   # Also init git with .gitignore
  gt install ~/gt --github=user/repo      # Also create GitHub repo`,
	Args: cobra.MaximumNArgs(1),
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVarP(&installForce, "force", "f", false, "Overwrite existing harness")
	installCmd.Flags().StringVarP(&installName, "name", "n", "", "Town name (defaults to directory name)")
	installCmd.Flags().BoolVar(&installNoBeads, "no-beads", false, "Skip town beads initialization")
	installCmd.Flags().BoolVar(&installGit, "git", false, "Initialize git with .gitignore")
	installCmd.Flags().StringVar(&installGitHub, "github", "", "Create GitHub repo (format: owner/repo)")
	installCmd.Flags().BoolVar(&installPrivate, "private", false, "Make GitHub repo private (use with --github)")
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	// Determine target path
	targetPath := "."
	if len(args) > 0 {
		targetPath = args[0]
	}

	// Expand ~ and resolve to absolute path
	if targetPath[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("getting home directory: %w", err)
		}
		targetPath = filepath.Join(home, targetPath[1:])
	}

	absPath, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	// Determine town name
	townName := installName
	if townName == "" {
		townName = filepath.Base(absPath)
	}

	// Check if already a workspace
	if isWS, _ := workspace.IsWorkspace(absPath); isWS && !installForce {
		return fmt.Errorf("directory is already a Gas Town harness (use --force to reinitialize)")
	}

	// Check if inside an existing workspace
	if existingRoot, _ := workspace.Find(absPath); existingRoot != "" && existingRoot != absPath {
		fmt.Printf("%s Warning: Creating harness inside existing workspace at %s\n",
			style.Dim.Render("⚠"), existingRoot)
	}

	fmt.Printf("%s Creating Gas Town harness at %s\n\n",
		style.Bold.Render("🏭"), style.Dim.Render(absPath))

	// Create directory structure
	if err := os.MkdirAll(absPath, 0755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	// Create mayor directory (holds config, state, and mail)
	mayorDir := filepath.Join(absPath, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		return fmt.Errorf("creating mayor directory: %w", err)
	}
	fmt.Printf("   ✓ Created mayor/\n")

	// Create town.json in mayor/
	townConfig := &config.TownConfig{
		Type:      "town",
		Version:   config.CurrentTownVersion,
		Name:      townName,
		CreatedAt: time.Now(),
	}
	townPath := filepath.Join(mayorDir, "town.json")
	if err := config.SaveTownConfig(townPath, townConfig); err != nil {
		return fmt.Errorf("writing town.json: %w", err)
	}
	fmt.Printf("   ✓ Created mayor/town.json\n")

	// Create rigs.json in mayor/
	rigsConfig := &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    make(map[string]config.RigEntry),
	}
	rigsPath := filepath.Join(mayorDir, "rigs.json")
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		return fmt.Errorf("writing rigs.json: %w", err)
	}
	fmt.Printf("   ✓ Created mayor/rigs.json\n")

	// Create rigs directory (for managed rig clones)
	rigsDir := filepath.Join(absPath, "rigs")
	if err := os.MkdirAll(rigsDir, 0755); err != nil {
		return fmt.Errorf("creating rigs directory: %w", err)
	}
	fmt.Printf("   ✓ Created rigs/\n")

	// Create mayor state.json
	mayorState := &config.AgentState{
		Role:       "mayor",
		LastActive: time.Now(),
	}
	statePath := filepath.Join(mayorDir, "state.json")
	if err := config.SaveAgentState(statePath, mayorState); err != nil {
		return fmt.Errorf("writing mayor state: %w", err)
	}
	fmt.Printf("   ✓ Created mayor/state.json\n")

	// Create Mayor CLAUDE.md at harness root (Mayor runs from there)
	if err := createMayorCLAUDEmd(absPath, absPath); err != nil {
		fmt.Printf("   %s Could not create CLAUDE.md: %v\n", style.Dim.Render("⚠"), err)
	} else {
		fmt.Printf("   ✓ Created CLAUDE.md\n")
	}

	// Initialize town-level beads database (optional)
	// Town beads (gm- prefix) stores mayor mail, cross-rig coordination, and handoffs.
	// Rig beads are separate and have their own prefixes.
	if !installNoBeads {
		if err := initTownBeads(absPath); err != nil {
			fmt.Printf("   %s Could not initialize town beads: %v\n", style.Dim.Render("⚠"), err)
		} else {
			fmt.Printf("   ✓ Initialized .beads/ (town-level beads with gm- prefix)\n")
		}
	}

	// Initialize git if requested (--git or --github implies --git)
	if installGit || installGitHub != "" {
		fmt.Println()
		if err := InitGitForHarness(absPath, installGitHub, installPrivate); err != nil {
			return fmt.Errorf("git initialization failed: %w", err)
		}
	}

	fmt.Printf("\n%s Harness created successfully!\n", style.Bold.Render("✓"))
	fmt.Println()
	fmt.Println("Next steps:")
	step := 1
	if !installGit && installGitHub == "" {
		fmt.Printf("  %d. Initialize git: %s\n", step, style.Dim.Render("gt git-init"))
		step++
	}
	fmt.Printf("  %d. Add a rig: %s\n", step, style.Dim.Render("gt rig add <name> <git-url>"))
	step++
	fmt.Printf("  %d. Start the Mayor: %s\n", step, style.Dim.Render("cd "+absPath+" && gt prime"))

	return nil
}

func createMayorCLAUDEmd(harnessRoot, townRoot string) error {
	tmpl, err := templates.New()
	if err != nil {
		return err
	}

	data := templates.RoleData{
		Role:     "mayor",
		TownRoot: townRoot,
		WorkDir:  harnessRoot,
	}

	content, err := tmpl.RenderRole("mayor", data)
	if err != nil {
		return err
	}

	claudePath := filepath.Join(harnessRoot, "CLAUDE.md")
	return os.WriteFile(claudePath, []byte(content), 0644)
}

func writeJSON(path string, data interface{}) error {
	content, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

// initTownBeads initializes town-level beads database using bd init.
// Town beads use the "gm-" prefix for mayor mail and cross-rig coordination.
func initTownBeads(townPath string) error {
	// Run: bd init --prefix gm
	cmd := exec.Command("bd", "init", "--prefix", "gm")
	cmd.Dir = townPath

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if beads is already initialized
		if strings.Contains(string(output), "already initialized") {
			return nil // Already initialized is fine
		}
		return fmt.Errorf("bd init failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

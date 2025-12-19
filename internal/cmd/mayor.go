package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// MayorSessionName is the tmux session name for the Mayor.
const MayorSessionName = "gt-mayor"

var mayorCmd = &cobra.Command{
	Use:     "mayor",
	Aliases: []string{"may"},
	Short:   "Manage the Mayor session",
	Long: `Manage the Mayor tmux session.

The Mayor is the global coordinator for Gas Town, running as a persistent
tmux session. Use the subcommands to start, stop, attach, and check status.`,
}

var mayorStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Mayor session",
	Long: `Start the Mayor tmux session.

Creates a new detached tmux session for the Mayor and launches Claude.
The session runs in the workspace root directory.`,
	RunE: runMayorStart,
}

var mayorStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Mayor session",
	Long: `Stop the Mayor tmux session.

Attempts graceful shutdown first (Ctrl-C), then kills the tmux session.`,
	RunE: runMayorStop,
}

var mayorAttachCmd = &cobra.Command{
	Use:     "attach",
	Aliases: []string{"at"},
	Short:   "Attach to the Mayor session",
	Long: `Attach to the running Mayor tmux session.

Attaches the current terminal to the Mayor's tmux session.
Detach with Ctrl-B D.`,
	RunE: runMayorAttach,
}

var mayorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check Mayor session status",
	Long:  `Check if the Mayor tmux session is currently running.`,
	RunE:  runMayorStatus,
}

var mayorRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the Mayor session",
	Long: `Restart the Mayor tmux session.

Stops the current session (if running) and starts a fresh one.`,
	RunE: runMayorRestart,
}

func init() {
	mayorCmd.AddCommand(mayorStartCmd)
	mayorCmd.AddCommand(mayorStopCmd)
	mayorCmd.AddCommand(mayorAttachCmd)
	mayorCmd.AddCommand(mayorStatusCmd)
	mayorCmd.AddCommand(mayorRestartCmd)

	rootCmd.AddCommand(mayorCmd)
}

func runMayorStart(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	// Check if session already exists
	running, err := t.HasSession(MayorSessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if running {
		return fmt.Errorf("Mayor session already running. Attach with: gt mayor attach")
	}

	if err := startMayorSession(t); err != nil {
		return err
	}

	fmt.Printf("%s Mayor session started. Attach with: %s\n",
		style.Bold.Render("✓"),
		style.Dim.Render("gt mayor attach"))

	return nil
}

// startMayorSession creates and initializes the Mayor tmux session.
func startMayorSession(t *tmux.Tmux) error {
	// Find workspace root
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Create session in workspace root
	fmt.Println("Starting Mayor session...")
	if err := t.NewSession(MayorSessionName, townRoot); err != nil {
		return fmt.Errorf("creating session: %w", err)
	}

	// Set environment
	t.SetEnvironment(MayorSessionName, "GT_ROLE", "mayor")

	// Launch Claude in a respawn loop - session survives restarts
	// The startup hook handles 'gt prime' automatically
	loopCmd := `while true; do echo "🏛️  Starting Mayor session..."; claude --dangerously-skip-permissions; echo ""; echo "Mayor exited. Restarting in 2s... (Ctrl-C to stop)"; sleep 2; done`
	if err := t.SendKeys(MayorSessionName, loopCmd); err != nil {
		return fmt.Errorf("sending command: %w", err)
	}

	return nil
}

func runMayorStop(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	// Check if session exists
	running, err := t.HasSession(MayorSessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return errors.New("Mayor session is not running")
	}

	fmt.Println("Stopping Mayor session...")

	// Try graceful shutdown first
	t.SendKeysRaw(MayorSessionName, "C-c")
	time.Sleep(100 * time.Millisecond)

	// Kill the session
	if err := t.KillSession(MayorSessionName); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	fmt.Printf("%s Mayor session stopped.\n", style.Bold.Render("✓"))
	return nil
}

func runMayorAttach(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	// Check if session exists
	running, err := t.HasSession(MayorSessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		// Auto-start if not running
		fmt.Println("Mayor session not running, starting...")
		if err := startMayorSession(t); err != nil {
			return err
		}
	}
	// Session uses a respawn loop, so Claude restarts automatically if it exits

	// Use exec to replace current process with tmux attach
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}

	return execCommand(tmuxPath, "attach-session", "-t", MayorSessionName)
}

// execCommand replaces the current process with the given command.
// This is used for attaching to tmux sessions.
func execCommand(name string, args ...string) error {
	// On Unix, we would use syscall.Exec to replace the process
	// For portability, we use exec.Command and wait
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runMayorStatus(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	running, err := t.HasSession(MayorSessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	if running {
		// Get session info for more details
		info, err := t.GetSessionInfo(MayorSessionName)
		if err == nil {
			status := "detached"
			if info.Attached {
				status = "attached"
			}
			fmt.Printf("%s Mayor session is %s\n",
				style.Bold.Render("●"),
				style.Bold.Render("running"))
			fmt.Printf("  Status: %s\n", status)
			fmt.Printf("  Created: %s\n", info.Created)
			fmt.Printf("\nAttach with: %s\n", style.Dim.Render("gt mayor attach"))
		} else {
			fmt.Printf("%s Mayor session is %s\n",
				style.Bold.Render("●"),
				style.Bold.Render("running"))
		}
	} else {
		fmt.Printf("%s Mayor session is %s\n",
			style.Dim.Render("○"),
			"not running")
		fmt.Printf("\nStart with: %s\n", style.Dim.Render("gt mayor start"))
	}

	return nil
}

func runMayorRestart(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()

	running, err := t.HasSession(MayorSessionName)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}

	if running {
		// Graceful restart: send Ctrl-C to exit Claude, loop will restart it
		fmt.Println("Restarting Mayor (sending Ctrl-C to trigger respawn loop)...")
		t.SendKeysRaw(MayorSessionName, "C-c")
		fmt.Printf("%s Mayor will restart automatically. Session stays attached.\n", style.Bold.Render("✓"))
		return nil
	}

	// Not running, start fresh
	return runMayorStart(cmd, args)
}

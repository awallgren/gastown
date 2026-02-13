// Package activity provides a "blinkenlights" TUI for Gas Town agent activity.
// Inspired by the Thinking Machines CM-5 LED panel - a dense grid of lights
// that shows at a glance whether the town is humming along.
package activity

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/steveyegge/gastown/internal/constants"
)

// ActivityLevel represents how recently an agent was active.
type ActivityLevel int

const (
	LevelActive      ActivityLevel = iota // activity timestamp changed in last 3s
	LevelRecent                           // changed in last 30s
	LevelWarm                             // changed in last 2m
	LevelCool                             // changed in last 5m
	LevelCold                             // no change in 5m+
	LevelRateLimited                      // hit rate limit
	LevelHitLimit                         // hit usage cap - agent dead until reset
	LevelWaitingForHuman                  // blocked waiting for human input
	LevelDead                             // no session
)

// AgentLight represents one "LED" on the panel.
type AgentLight struct {
	Name        string
	Icon        string
	Role        string
	Rig         string
	SessionName string

	// Tracking activity changes (is text scrolling?)
	CurActivity    int64     // current window_activity unix timestamp
	PrevActivity   int64     // previous poll's timestamp
	LastChangeTime time.Time // when we last saw the timestamp change
	Level          ActivityLevel

	// Pane-derived status (updated every poll)
	StatusText      string // current activity description from pane
	WaitingForHuman bool   // agent is blocked on human input
	WaitingReason   string // why waiting (e.g., "user prompt", "permission")
	RateLimited     bool   // pane shows rate limit message
	HitLimit        bool   // agent hit usage/token limit (dead until reset)
	LimitResetInfo  string // extracted reset info (e.g., "resets 2pm (America/Los_Angeles)")
	ContextPercent  int    // context remaining (0-100, 0=unknown)
	CurrentTool     string // currently executing tool/command (e.g., "Bash(git status)")
	SessionLimitPct int    // session usage percent (0=unknown, sticky)
	SessionLimitReset string // when the session limit resets (sticky)

	// Hover tooltip info
	CurrentBead   string // detected bead ID from pane content
	RecentOutput  string // last few lines of output
	renderY       int    // Y position in render (for hover detection)
	renderHeight  int    // height of rendered agent (for hover detection)
}

// Model is the bubbletea model for the blinkenlights TUI.
type Model struct {
	width  int
	height int

	// Agent lights organized by rig
	agents []*AgentLight
	rigs   []string // ordered rig names (hq first)

	// Animation state
	blinkOn  bool // toggles every tick for blink effect
	tickNum  int  // counts ticks for sparkle effects

	// Mouse hover state
	hoveredAgent *AgentLight // currently hovered agent
	mouseX       int
	mouseY       int

	// Stats
	totalAgents      int
	activeCount      int
	recentCount      int
	idleCount        int
	stuckCount       int
	rateLimitedCount int
	hitLimitCount    int
	waitingCount     int
}

// NewModel creates a new activity TUI model.
func NewModel() *Model {
	return &Model{
		agents: make([]*AgentLight, 0),
	}
}

// Init initializes the model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.pollSessions(),
		m.blinkTick(),
		tea.SetWindowTitle("GT Activity"),
		tea.EnableMouseAllMotion, // Enable mouse tracking
	)
}

// Message types
type (
	sessionsMsg struct {
		sessions []sessionInfo
	}
	blinkMsg  struct{}
	pollMsg   struct{}
)

type sessionInfo struct {
	name      string
	activity  int64
	paneLines []string // captured pane content for status extraction
}

// pollSessions queries tmux for all Gas Town session activity.
func (m *Model) pollSessions() tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}|#{window_activity}")
		out, err := cmd.Output()
		if err != nil {
			return sessionsMsg{sessions: nil}
		}

		var sessions []sessionInfo
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 2)
			if len(parts) != 2 {
				continue
			}
			name := parts[0]
			// Only Gas Town sessions
			if !strings.HasPrefix(name, "gt-") && !strings.HasPrefix(name, "hq-") {
				continue
			}
			var ts int64
			if _, err := fmt.Sscanf(parts[1], "%d", &ts); err != nil || ts == 0 {
				continue
			}
			sessions = append(sessions, sessionInfo{name: name, activity: ts})
		}

		// Capture pane content for each session (for status extraction)
		for i := range sessions {
			paneCmd := exec.Command("tmux", "capture-pane", "-t", sessions[i].name, "-p", "-S", "-10")
			paneOut, paneErr := paneCmd.Output()
			if paneErr == nil {
				sessions[i].paneLines = strings.Split(string(paneOut), "\n")
			}
		}

		return sessionsMsg{sessions: sessions}
	}
}

// blinkTick fires every 300ms for animation.
func (m *Model) blinkTick() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(t time.Time) tea.Msg {
		return blinkMsg{}
	})
}

// pollTick fires every 1s to re-poll tmux.
func (m *Model) pollTick() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg {
		return pollMsg{}
	})
}

// Update handles messages.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}

	case tea.MouseMsg:
		m.mouseX = msg.X
		m.mouseY = msg.Y
		m.updateHoveredAgent()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case sessionsMsg:
		m.updateAgents(msg.sessions)
		return m, m.pollTick()

	case blinkMsg:
		m.blinkOn = !m.blinkOn
		m.tickNum++
		return m, m.blinkTick()

	case pollMsg:
		return m, m.pollSessions()
	}

	return m, nil
}

// updateAgents merges new session data into the agent lights.
func (m *Model) updateAgents(sessions []sessionInfo) {
	now := time.Now()

	// Build lookup from current agents
	existing := make(map[string]*AgentLight)
	for _, a := range m.agents {
		existing[a.SessionName] = a
	}

	// Build new set from sessions
	seen := make(map[string]bool)
	for _, s := range sessions {
		seen[s.name] = true

		agent, ok := existing[s.name]
		if !ok {
			// New agent
			agent = &AgentLight{
				SessionName:    s.name,
				CurActivity:    s.activity,
				PrevActivity:   s.activity,
				LastChangeTime: now,
			}
			parseSessionName(agent)
			m.agents = append(m.agents, agent)
			existing[s.name] = agent
		} else {
			// Update existing
			agent.PrevActivity = agent.CurActivity
			agent.CurActivity = s.activity
			if agent.CurActivity != agent.PrevActivity {
				agent.LastChangeTime = now
			}
		}
	}

	// Remove dead agents (not seen in this poll)
	filtered := m.agents[:0]
	for _, a := range m.agents {
		if seen[a.SessionName] {
			filtered = append(filtered, a)
		}
	}
	m.agents = filtered

	// Build pane content lookup from session data
	paneMap := make(map[string][]string)
	for _, s := range sessions {
		paneMap[s.name] = s.paneLines
	}

	// Update activity levels and stats
	m.activeCount = 0
	m.recentCount = 0
	m.idleCount = 0
	m.stuckCount = 0
	m.rateLimitedCount = 0
	m.hitLimitCount = 0
	m.waitingCount = 0

	for _, a := range m.agents {
		// Parse pane content for status info
		if lines, ok := paneMap[a.SessionName]; ok {
			parsePaneContent(a, lines)
		}

		sinceLast := now.Sub(a.LastChangeTime)

		// Waiting-for-human overrides everything, but only if the agent
		// hasn't produced output recently (5s debounce avoids false positives
		// from brief prompt appearances between operations)
		if a.WaitingForHuman && sinceLast > 5*time.Second {
			a.Level = LevelWaitingForHuman
			m.waitingCount++
			continue
		}
		// Clear false positive if agent is still actively producing output
		if a.WaitingForHuman && sinceLast <= 5*time.Second {
			a.WaitingForHuman = false
		}

		// Hit-limit overrides time-based level - agent is dead until reset.
		// No debounce needed: the pattern is very specific and won't false-positive.
		if a.HitLimit {
			a.Level = LevelHitLimit
			m.hitLimitCount++
			continue
		}

		switch {
		case sinceLast < 3*time.Second:
			a.Level = LevelActive
			m.activeCount++
		case sinceLast < 30*time.Second:
			a.Level = LevelRecent
			m.recentCount++
		case sinceLast < 2*time.Minute:
			a.Level = LevelWarm
			m.idleCount++
		case sinceLast < 5*time.Minute:
			a.Level = LevelCool
			m.idleCount++
		default:
			a.Level = LevelCold
			m.stuckCount++
		}

		// Rate limit override (pane-derived) - only for non-active agents
		if a.RateLimited && a.Level != LevelActive && a.Level != LevelRecent {
			switch a.Level {
			case LevelWarm, LevelCool:
				m.idleCount--
			case LevelCold:
				m.stuckCount--
			}
			a.Level = LevelRateLimited
			m.rateLimitedCount++
		}
	}
	m.totalAgents = len(m.agents)

	// Rebuild rig ordering
	m.rebuildRigOrder()
}

// parseSessionName extracts role/rig/name from a session name.
func parseSessionName(a *AgentLight) {
	name := a.SessionName

	if strings.HasPrefix(name, "hq-") {
		a.Rig = "hq"
		suffix := strings.TrimPrefix(name, "hq-")
		switch suffix {
		case "mayor":
			a.Role = constants.RoleMayor
			a.Name = "Mayor"
			a.Icon = constants.EmojiMayor
		case "deacon":
			a.Role = constants.RoleDeacon
			a.Name = "Deacon"
			a.Icon = constants.EmojiDeacon
		default:
			a.Role = suffix
			a.Name = suffix
			a.Icon = "?"
		}
		return
	}

	// gt-<rig>-<rest>
	if !strings.HasPrefix(name, "gt-") {
		a.Name = name
		return
	}

	suffix := strings.TrimPrefix(name, "gt-")

	// Dog sessions: gt-dog-{name} (config pattern) - town-level, no rig
	if strings.HasPrefix(suffix, "dog-") {
		a.Rig = "hq" // town-level agents shown alongside mayor/deacon
		a.Role = constants.RoleDog
		a.Name = strings.TrimPrefix(suffix, "dog-")
		a.Icon = constants.EmojiDog
		return
	}

	parts := strings.SplitN(suffix, "-", 2)
	if len(parts) < 2 {
		a.Name = suffix
		return
	}

	a.Rig = parts[0]
	rest := parts[1]

	switch {
	case rest == "witness":
		a.Role = constants.RoleWitness
		a.Name = "witness"
		a.Icon = constants.EmojiWitness
	case rest == "refinery":
		a.Role = constants.RoleRefinery
		a.Name = "refinery"
		a.Icon = constants.EmojiRefinery
	case strings.HasPrefix(rest, "crew-"):
		a.Role = constants.RoleCrew
		a.Name = strings.TrimPrefix(rest, "crew-")
		a.Icon = constants.EmojiCrew
	// Dog sessions: gt-{town}-deacon-{name} (session_manager pattern)
	case strings.HasPrefix(rest, "deacon-"):
		a.Role = constants.RoleDog
		a.Name = strings.TrimPrefix(rest, "deacon-")
		a.Icon = constants.EmojiDog
	default:
		a.Role = constants.RolePolecat
		a.Name = rest
		a.Icon = constants.EmojiPolecat
	}
}

// rebuildRigOrder produces a sorted list of rig names, hq first.
func (m *Model) rebuildRigOrder() {
	rigSet := make(map[string]bool)
	for _, a := range m.agents {
		if a.Rig != "" {
			rigSet[a.Rig] = true
		}
	}

	m.rigs = nil
	if rigSet["hq"] {
		m.rigs = append(m.rigs, "hq")
	}
	var others []string
	for rig := range rigSet {
		if rig != "hq" {
			others = append(others, rig)
		}
	}
	sort.Strings(others)
	m.rigs = append(m.rigs, others...)
}

// agentsForRig returns agents belonging to a rig in display order.
func (m *Model) agentsForRig(rig string) []*AgentLight {
	roleOrder := map[string]int{
		constants.RoleMayor:    0,
		constants.RoleDeacon:   1,
		constants.RoleDog:      2,
		constants.RoleWitness:  3,
		constants.RoleRefinery: 4,
		constants.RoleCrew:     5,
		constants.RolePolecat:  6,
	}

	var agents []*AgentLight
	for _, a := range m.agents {
		if a.Rig == rig {
			agents = append(agents, a)
		}
	}

	// Sort by role priority, then name
	for i := 0; i < len(agents); i++ {
		for j := i + 1; j < len(agents); j++ {
			oi := roleOrder[agents[i].Role]
			oj := roleOrder[agents[j].Role]
			if oi > oj || (oi == oj && agents[i].Name > agents[j].Name) {
				agents[i], agents[j] = agents[j], agents[i]
			}
		}
	}
	return agents
}

// isChromeLine returns true if the line is Claude Code TUI chrome that should
// be ignored when extracting agent status. These elements are always present
// at the bottom of a Claude Code session and carry no status information.
func isChromeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	// Claude Code input prompt (always visible at bottom, not a status signal)
	if strings.HasPrefix(trimmed, "❯") {
		return true
	}
	// Claude Code bottom status bar indicators
	if strings.Contains(trimmed, "⏵") {
		return true
	}
	// Status bar text (bypass permissions, auto-accept, etc.)
	if strings.Contains(trimmed, "bypass permissions") ||
		strings.Contains(trimmed, "shift+tab to cycle") ||
		strings.Contains(trimmed, "esc to interrupt") ||
		strings.Contains(trimmed, "auto-accept") {
		return true
	}
	// Separator lines (all box-drawing horizontal characters)
	if isSeparatorLine(trimmed) {
		return true
	}
	return false
}

// isSeparatorLine checks if a string is entirely horizontal line characters.
func isSeparatorLine(s string) bool {
	if len(s) < 4 {
		return false
	}
	for _, r := range s {
		if r != '─' && r != '━' && r != '—' && r != '-' && r != '═' {
			return false
		}
	}
	return true
}

// extractTaskName pulls the conversation/task name from a Claude Code status bar line.
// Format: "⏵⏵ bypass permissions on · Implement CWE enricher source (running) · esc to interrupt"
// Returns: "Implement CWE enricher source"
// Uses text anchors (not symbol chars) since tmux capture may render ⏵ as different codepoints.
func extractTaskName(line string) string {
	trimmed := strings.TrimSpace(line)

	// The status bar always ends with "esc to interrupt"
	escIdx := strings.Index(trimmed, "esc to interrupt")
	if escIdx < 0 {
		return ""
	}

	// Find where the permission mode segment ends
	var permEndIdx int
	found := false
	for _, marker := range []string{
		"bypass permissions on",
		"auto-accept edits on",
		"auto-accept all on",
	} {
		if idx := strings.Index(trimmed, marker); idx >= 0 {
			permEndIdx = idx + len(marker)
			found = true
			break
		}
	}
	if !found {
		return ""
	}

	if permEndIdx >= escIdx {
		return ""
	}

	// Everything between permission mode and "esc to interrupt" is the task name
	middle := trimmed[permEndIdx:escIdx]

	// Skip the "(shift+tab to cycle)" format which means no task name
	if strings.Contains(middle, "shift+tab") {
		return ""
	}

	// Strip separator chars from both ends (·, •, ∙, dashes, whitespace)
	middle = strings.Trim(middle, " \t·•∙‧⋅─━—-|/")
	if middle == "" {
		return ""
	}

	// Strip redundant "(running)" suffix - the LED bar already shows activity
	middle = strings.TrimSuffix(middle, "(running)")
	middle = strings.TrimSpace(middle)

	return middle
}

// parsePaneContent analyzes captured pane lines to extract status information.
// Lines are ordered top-to-bottom (time flows downward). We first strip Claude
// Code UI chrome from the bottom, then scan upward from the most recent real
// content to find status signals.
func parsePaneContent(a *AgentLight, lines []string) {
	a.StatusText = ""
	a.WaitingForHuman = false
	a.WaitingReason = ""
	a.RateLimited = false
	a.HitLimit = false
	a.LimitResetInfo = ""
	a.CurrentTool = "" // Reset each poll - stale tools cause false display
	// ContextPercent persists until updated (sticky)
	// SessionLimitPct and SessionLimitReset persist until updated (sticky)

	if len(lines) == 0 {
		return
	}

	// Extract task name from Claude Code status bar (before chrome filtering,
	// since the status bar IS chrome but contains the task name)
	taskName := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if tn := extractTaskName(lines[i]); tn != "" {
			taskName = tn
			break
		}
	}

	// Check all captured lines for usage limit hit (very specific pattern,
	// safe to scan all 10 lines). Check narrower window for temporary rate limits.
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		// Usage limit: "You've hit your limit · resets 2pm (America/Los_Angeles)"
		// Match "hit your limit" without the contraction to handle curly apostrophe
		// (Claude Code may render ' as U+2019 which breaks exact "you've" matching)
		if strings.Contains(lower, "hit your limit") {
			a.HitLimit = true
			a.LimitResetInfo = extractLimitResetInfo(trimmed)
			break
		}
		// Credit/billing limit: "Credit balance too low · Add funds: ..."
		if strings.Contains(lower, "credit balance too low") || strings.Contains(lower, "add funds:") {
			a.HitLimit = true
			a.LimitResetInfo = "credit balance too low"
			break
		}
		// Secondary signal: "/extra-usage to finish what you're working on"
		if strings.Contains(lower, "/extra-usage") {
			a.HitLimit = true
			break
		}

		// Session limit warning: "You've used 95% of your session limit · resets 8pm (America/Los_Angeles)"
		// Match on "of your session limit" to avoid curly apostrophe issue with "you've"
		if strings.Contains(lower, "of your session limit") || strings.Contains(lower, "% of your session") {
			if pct, reset := extractSessionLimit(trimmed); pct > 0 {
				a.SessionLimitPct = pct
				if reset != "" {
					a.SessionLimitReset = reset
				}
			}
		}

		// Context percentage: "Context left until auto-compact: 20%"
		if strings.Contains(lower, "context left until auto-compact:") {
			if pct := extractContextPercent(trimmed); pct > 0 {
				a.ContextPercent = pct
			}
		}

		// Current tool execution: "⏺ Bash(git status)" or "● Read(file.go)"
		// When compaction starts, clear context percent
		if tool := extractCurrentTool(trimmed); tool != "" {
			a.CurrentTool = tool
			// Clear context when compaction starts (will naturally update after)
			if strings.Contains(strings.ToLower(tool), "compact") {
				a.ContextPercent = 0
			}
		}
	}

	// Temporary API rate limit (narrower window, only if not already hit-limit)
	if !a.HitLimit {
		start := len(lines) - 3
		if start < 0 {
			start = 0
		}
		for _, line := range lines[start:] {
			lower := strings.ToLower(strings.TrimSpace(line))
			if strings.Contains(lower, "rate limit") && strings.Contains(lower, "resets") {
				a.RateLimited = true
				break
			}
		}
	}

	// Scan from bottom upward, skipping chrome lines.
	// Only check a limited window of real content lines to avoid
	// false positives from stale output higher in the pane.
	contentChecked := 0
	for i := len(lines) - 1; i >= 0 && contentChecked < 8; i-- {
		if isChromeLine(lines[i]) {
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		contentChecked++

		// Check for waiting-for-human patterns (highest priority)
		if reason := detectHumanWait(trimmed); reason != "" {
			a.WaitingForHuman = true
			a.WaitingReason = reason
			return
		}

		// Check for Claude Code status line with timing/spinner info
		if status := extractStatusLine(trimmed); status != "" {
			a.StatusText = status
			break
		}

		// Don't use unrecognized lines as fallback - they're usually tool
		// output noise (raw commands, file paths, etc.) that isn't useful
		// in the blink display. Let the level-based label handle it.
	}

	// Compaction overrides stale tool calls - agent has moved past tool execution
	if strings.HasPrefix(a.StatusText, "COMPACTING") {
		a.CurrentTool = ""
		a.ContextPercent = 0
	}

	// HitLimit overrides stale tool calls - agent is dead, last tool is noise
	if a.HitLimit {
		a.CurrentTool = ""
	}

	// Append task name to status (gives context about WHAT the agent is working on)
	if taskName != "" {
		if a.StatusText != "" {
			a.StatusText += " · " + taskName
		} else {
			a.StatusText = taskName
		}
	}
}

// detectHumanWait checks if a line indicates the agent is waiting for human input.
// NOTE: The ❯ prompt is NOT checked here - it's always visible in Claude Code's
// TUI regardless of whether the agent is working. Only explicit interactive
// prompts (permissions, confirmations, questions, interruptions) count.
func detectHumanWait(line string) string {
	trimmed := strings.TrimSpace(line)

	// Strip leading tree-drawing characters (⎿, └, etc.) so we can match
	// content that appears as sub-output of tool calls
	cleaned := strings.TrimLeft(trimmed, "⎿└│├─ ")
	cleaned = strings.TrimSpace(cleaned)

	// Claude Code interruption: "Interrupted · What should Claude do instead?"
	if strings.HasPrefix(cleaned, "Interrupted") {
		return "interrupted"
	}

	// Tool permission: "Allow Bash: ..." or "Allow Read: ..."
	if strings.HasPrefix(cleaned, "Allow ") && strings.Contains(cleaned, "?") {
		if idx := strings.Index(cleaned, ":"); idx > 6 {
			tool := cleaned[6:idx]
			return "permission: " + tool
		}
		return "permission prompt"
	}

	// Yes/No confirmation prompts
	if strings.Contains(cleaned, "(Y)es") || strings.Contains(cleaned, "(y/n)") || strings.Contains(cleaned, "(Y/n)") {
		return "confirmation prompt"
	}

	// AskUserQuestion: starts with "? " at the ORIGINAL line start (not after
	// tree chars, to avoid false positives from beads output like "⎿ ? bead-id")
	if strings.HasPrefix(trimmed, "? ") && len(trimmed) > 2 {
		return "question"
	}

	// Explicit waiting patterns
	lower := strings.ToLower(cleaned)
	if strings.Contains(lower, "do you want to") || strings.Contains(lower, "press enter") {
		return "waiting for confirmation"
	}

	return ""
}

// extractStatusLine extracts meaningful status from a pane line.
// Only matches high-signal patterns: parenthetical stats (timer/tokens) and
// spinner/✳ indicators. Ignores generic output to avoid showing noise.
func extractStatusLine(line string) string {
	// Strip leading tree-drawing characters (⎿, └, │) from tool sub-output
	cleaned := strings.TrimSpace(line)
	cleaned = strings.TrimLeft(cleaned, "⎿└│├─ ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return ""
	}

	// Priority 1: Parenthetical stats (timer · tokens · thought)
	// e.g., "✳ Newspapering… (9m 20s · ↓ 6.8k tokens · thought for 6s)"
	// → extracts "9m 20s · ↓ 6.8k tokens · thought for 6s"
	// Some actions are operationally significant and get surfaced by name.
	if stats := extractParenStats(cleaned); stats != "" {
		if strings.Contains(cleaned, "Compacting") || strings.Contains(cleaned, "compacting") {
			return "COMPACTING · " + stats
		}
		return stats
	}

	// Priority 2: Lines with ✳ or braille spinner (active operation)
	spinners := "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷✳"
	for _, r := range cleaned {
		if strings.ContainsRune(spinners, r) {
			idx := strings.IndexRune(cleaned, r)
			rest := strings.TrimSpace(cleaned[idx+len(string(r)):])
			if rest != "" {
				return truncateStatus(rest)
			}
			return "working"
		}
	}

	return ""
}

// extractParenStats extracts timing/token stats from a parenthetical in a line.
// Only matches if the content looks like stats (contains · separators).
func extractParenStats(line string) string {
	start := strings.LastIndex(line, "(")
	end := strings.LastIndex(line, ")")
	if start < 0 || end <= start+1 {
		return ""
	}
	inner := strings.TrimSpace(line[start+1 : end])
	// Must contain · separator to look like Claude Code stats
	if strings.Contains(inner, "·") {
		return inner
	}
	return ""
}

// extractLimitResetInfo pulls the reset time from a usage limit message.
// Input:  "You've hit your limit · resets 2pm (America/Los_Angeles)"
// Output: "resets 2pm (America/Los_Angeles)"
func extractLimitResetInfo(line string) string {
	lower := strings.ToLower(line)
	idx := strings.Index(lower, "resets ")
	if idx < 0 {
		return ""
	}
	// Return from "resets" onward, trimming trailing whitespace
	info := strings.TrimSpace(line[idx:])
	return info
}

// extractContextPercent extracts the context percentage from Claude's auto-compact warning.
// Input:  "Context left until auto-compact: 20%"
// Output: 20
func extractContextPercent(line string) int {
	lower := strings.ToLower(line)
	idx := strings.Index(lower, "context left until auto-compact:")
	if idx < 0 {
		return 0
	}
	// Find the percentage after the colon
	rest := line[idx+len("context left until auto-compact:"):]
	rest = strings.TrimSpace(rest)
	// Extract digits before '%'
	pctIdx := strings.Index(rest, "%")
	if pctIdx < 0 {
		return 0
	}
	numStr := strings.TrimSpace(rest[:pctIdx])
	var pct int
	if _, err := fmt.Sscanf(numStr, "%d", &pct); err == nil && pct >= 0 && pct <= 100 {
		return pct
	}
	return 0
}

// extractSessionLimit extracts usage percentage and reset time from a session limit warning.
// Input:  "You've used 95% of your session limit · resets 8pm (America/Los_Angeles)"
// Output: 95, "resets 8pm (America/Los_Angeles)"
func extractSessionLimit(line string) (int, string) {
	lower := strings.ToLower(line)
	idx := strings.Index(lower, "you've used ")
	if idx < 0 {
		return 0, ""
	}
	rest := line[idx+len("you've used "):]
	// Extract percentage: "95% of your session limit"
	pctIdx := strings.Index(rest, "%")
	if pctIdx < 0 {
		return 0, ""
	}
	numStr := strings.TrimSpace(rest[:pctIdx])
	var pct int
	if _, err := fmt.Sscanf(numStr, "%d", &pct); err != nil || pct < 0 || pct > 100 {
		return 0, ""
	}
	// Extract reset info
	reset := extractLimitResetInfo(line)
	return pct, reset
}

// extractCurrentTool extracts the currently executing tool/command from Claude's output.
// Input:  "⏺ Bash(git stash list)" or "● Read(model.go)"
// Output: "Bash(git stash list)"
func extractCurrentTool(line string) string {
	trimmed := strings.TrimSpace(line)
	// Check for tool execution indicators at start of line
	// Common indicators: ⏺ (record), ● (bullet), ⏵ (play), etc.
	if len(trimmed) == 0 {
		return ""
	}
	firstRune := []rune(trimmed)[0]
	// Tool execution indicators
	indicators := "⏺●⏵◉○◎⦿"
	if !strings.ContainsRune(indicators, firstRune) {
		return ""
	}
	// Extract everything after the indicator
	rest := strings.TrimSpace(string([]rune(trimmed)[1:]))
	if rest == "" {
		return ""
	}

	// Tool format MUST be: CapitalizedToolName(args)
	// This excludes chrome like "● ▶ bypass permissions" (no capital letter start)
	// Valid examples: "Bash(git status)", "Read(file.go)", "WebFetch(url)"
	if len(rest) < 2 {
		return ""
	}
	// First character must be uppercase (tool names are always capitalized)
	firstChar := rune(rest[0])
	if firstChar < 'A' || firstChar > 'Z' {
		return "" // Not a tool - probably chrome
	}
	// Must have opening paren (all tools have args)
	parenIdx := strings.Index(rest, "(")
	if parenIdx < 0 || parenIdx > 20 {
		return "" // No paren or name too long - not a tool
	}

	// Valid tool execution - truncate if too long
	if len(rest) > 60 {
		// Try to truncate at end of args if possible
		if closeIdx := strings.Index(rest, ")"); closeIdx > 0 && closeIdx < 60 {
			rest = rest[:closeIdx+1]
		} else {
			rest = rest[:57] + "..."
		}
	}
	return rest
}

// truncateStatus does a light safety trim. Real truncation happens in
// renderLight based on actual terminal width.
func truncateStatus(s string) string {
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}

// updateHoveredAgent determines which agent (if any) the mouse is hovering over.
func (m *Model) updateHoveredAgent() {
	m.hoveredAgent = nil
	for _, a := range m.agents {
		if a.renderY > 0 && m.mouseY >= a.renderY && m.mouseY < a.renderY+a.renderHeight {
			m.hoveredAgent = a
			m.fetchAgentDetails(a)
			break
		}
	}
}

// fetchAgentDetails fetches additional info for hover tooltip.
func (m *Model) fetchAgentDetails(a *AgentLight) {
	// Capture last 20 lines to extract bead IDs and recent activity
	cmd := exec.Command("tmux", "capture-pane", "-t", a.SessionName, "-p", "-S", "-20")
	out, err := cmd.Output()
	if err != nil {
		return
	}

	content := string(out)

	// Extract bead IDs (pattern: word-shortid, e.g., wp-abc123, gp-xyz789)
	// Look for the most recent bead ID
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if beadID := extractBeadID(line); beadID != "" {
			a.CurrentBead = beadID
			break
		}
	}

	// Store last few non-empty lines for tooltip
	var recent []string
	for i := len(lines) - 1; i >= 0 && len(recent) < 3; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && line != "❯" {
			recent = append([]string{line}, recent...)
		}
	}
	if len(recent) > 0 {
		a.RecentOutput = strings.Join(recent, "\n")
	}
}

// extractBeadID extracts a bead ID from a line of text.
// Bead IDs follow the pattern: {prefix}-{shortid} like wp-abc123, gp-xyz789
func extractBeadID(line string) string {
	// Common patterns in Gas Town output
	// "bd show wp-abc123", "Closed wp-abc123", "wp-abc123:", etc.
	words := strings.Fields(line)
	for _, word := range words {
		// Strip trailing punctuation
		word = strings.TrimRight(word, ",:;.!?")
		// Check if it matches bead ID pattern: 2-3 letters, dash, 5-6 alphanumeric
		if len(word) >= 7 && len(word) <= 12 {
			parts := strings.Split(word, "-")
			if len(parts) == 2 && len(parts[0]) >= 2 && len(parts[0]) <= 3 && len(parts[1]) >= 5 {
				return word
			}
		}
	}
	return ""
}

// View renders the TUI.
func (m *Model) View() string {
	return m.render()
}

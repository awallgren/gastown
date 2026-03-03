// Package activity provides a "blinkenlights" TUI for Gas Town agent activity.
// Inspired by the Thinking Machines CM-5 LED panel - a dense grid of lights
// that shows at a glance whether the town is humming along.
package activity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/workspace"
)

// ActivityLevel represents how recently an agent was active.
type ActivityLevel int

const (
	LevelActive          ActivityLevel = iota // activity timestamp changed in last 3s
	LevelRecent                               // changed in last 30s
	LevelWarm                                 // changed in last 2m
	LevelCool                                 // changed in last 5m
	LevelCold                                 // no change in 5m+
	LevelRateLimited                          // hit rate limit
	LevelHitLimit                             // hit usage cap - agent dead until reset
	LevelWaitingForHuman                      // blocked waiting for human input
	LevelDead                                 // no session
)

// AgentLight represents one "LED" on the panel.
type AgentLight struct {
	Name        string
	Icon        string
	Role        string
	Rig         string
	SessionName string
	AgentType   string // "claude", "opencode", "gemini", etc. (cached, read once from GT_AGENT)

	// Tracking activity changes (is text scrolling?)
	CurActivity    int64     // current window_activity unix timestamp
	PrevActivity   int64     // previous poll's timestamp
	LastChangeTime time.Time // when we last saw the timestamp change
	Level          ActivityLevel

	// Pane-derived status (updated every poll)
	StatusText        string // current activity description from pane
	WaitingForHuman   bool   // agent is blocked on human input
	WaitingReason     string // why waiting (e.g., "user prompt", "permission")
	RateLimited       bool   // pane shows rate limit message
	HitLimit          bool   // agent hit usage/token limit (dead until reset)
	LimitResetInfo    string // extracted reset info (e.g., "resets 2pm (America/Los_Angeles)")
	ContextPercent    int    // context remaining (0-100, 0=unknown); displayed as "used" (100-value)
	TokenCount        int    // total tokens used in session (from pane header/sidebar, sticky)
	CurrentTool       string // currently executing tool/command (e.g., "Bash(git status)")
	SessionLimitPct   int    // session usage percent (0=unknown, sticky)
	SessionLimitReset string // when the session limit resets (sticky)
	IsCompacting      bool   // sticky: compaction detected, persists until cleared
	PreCompactCtxPct  int    // context% snapshot from when compaction started, for drop detection
	PrevStatusText    string // previous cycle's StatusText, used to avoid "streaming" clobbering useful info

	// Work tracking (from beads DB, updated on slower cadence)
	WorkBeadID    string // assigned/hooked bead ID (e.g., "wp-abc123")
	WorkBeadTitle string // bead title (e.g., "Fix login bug")
	FormulaName   string // attached formula name (e.g., "mol-polecat-work"), empty if none
	StepCurrent   string // current step title (e.g., "branch-setup"), empty if no molecule
	StepsDone     int    // completed steps in molecule
	StepsTotal    int    // total steps in molecule
	AgentState    string // agent lifecycle state from bead (e.g., "working", "idle", "stuck")
	LastPatrol    string // last patrol summary (sticky — persists until replaced by newer patrol)

	// Hover tooltip info
	RecentOutput   string    // last few lines of output
	SessionCreated time.Time // when the tmux session was created (for uptime)
	renderY        int       // Y position in render (for hover detection)
	renderHeight   int       // height of rendered agent (for hover detection)
}

// Model is the bubbletea model for the blinkenlights TUI.
type Model struct {
	width  int
	height int

	// Agent lights organized by rig
	agents []*AgentLight
	rigs   []string // ordered rig names (hq first)

	// Animation state
	blinkOn bool // toggles every tick for blink effect
	tickNum int  // counts ticks for sparkle effects

	// Mouse hover state
	hoveredAgent *AgentLight // currently hovered agent
	mouseX       int
	mouseY       int

	// Double-click detection (bubbletea has no native double-click)
	lastClickAgent *AgentLight // agent that was last left-clicked
	lastClickTime  time.Time   // when the last left-click occurred

	// Status flash message (e.g., "Opened terminal for gt-foo-crew-bar")
	flashMessage string    // message to display briefly
	flashTime    time.Time // when the flash was set

	// Town info
	townRoot string // cached town root for reading events file
	townName string // display name from town.json (e.g., "My Town")

	// Beads work tracking (slower cadence than tmux polls)
	lastBeadsPoll time.Time                   // when we last queried beads DB
	rigBeadsDirs  map[string]string           // rig name -> beads dir path (cached)
	patrolCache   map[string]patrolCacheEntry // "rig/role" -> cached patrol summary

	// Poll configuration
	pollInterval time.Duration // how often to poll tmux sessions (default 3s)

	// Plugin event consumption (for non-Claude agents like OpenCode)
	recentToolEvents []toolEvent // recent tool_started events (< 15s old)

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
// pollInterval controls how often tmux sessions are polled; 0 uses the default (3s).
func NewModel(pollInterval time.Duration) *Model {
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	// Best-effort town root discovery for reading events file.
	// Try workspace detection from CWD first, then fall back to env vars.
	// gt top can be run from anywhere (not just inside the town), so the
	// GT_TOWN_ROOT / GT_ROOT env vars set by shell integration are critical.
	townRoot := detectTownRoot()

	var townName string
	if townRoot != "" {
		// Initialize session prefix registry so IsKnownSession and
		// ParseSessionName can resolve rig-specific prefixes (e.g., "wi-"
		// for winnow, "wp-" for winnow_pm) instead of only matching "hq-".
		_ = session.InitRegistry(townRoot)

		// Load town display name from town.json.
		if tc, err := config.LoadTownConfig(constants.MayorTownPath(townRoot)); err == nil {
			townName = tc.Name
		}

		// Ensure GT_DOLT_PORT is set so bd CLI connects to the correct
		// Dolt server. Without this, bd falls back to dolt-server.port
		// files in each .beads/ dir which may contain stale port numbers
		// from previous server instances. DefaultConfig reads the
		// GT_DOLT_PORT env var (or uses the default 3307), matching how
		// "gt dolt status" discovers the running server.
		if os.Getenv("GT_DOLT_PORT") == "" {
			doltCfg := doltserver.DefaultConfig(townRoot)
			os.Setenv("GT_DOLT_PORT", strconv.Itoa(doltCfg.Port))
		}
	}

	return &Model{
		agents:       make([]*AgentLight, 0),
		townRoot:     townRoot,
		townName:     townName,
		pollInterval: pollInterval,
	}
}

// detectTownRoot finds the town root directory using multiple strategies.
// Priority: 1) workspace detection from CWD, 2) GT_TOWN_ROOT env var,
// 3) GT_ROOT env var, 4) shell integration cache (~/.cache/gastown/rigs.cache).
// Each candidate is validated by checking for mayor/town.json or a mayor/ directory.
func detectTownRoot() string {
	// Try workspace detection from CWD (works when inside the town tree).
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" {
		return townRoot
	}

	// Fallback: env vars set by shell integration or session manager.
	for _, envName := range []string{"GT_TOWN_ROOT", "GT_ROOT"} {
		if envRoot := os.Getenv(envName); envRoot != "" {
			if _, err := os.Stat(filepath.Join(envRoot, workspace.PrimaryMarker)); err == nil {
				return envRoot
			}
			if info, err := os.Stat(filepath.Join(envRoot, workspace.SecondaryMarker)); err == nil && info.IsDir() {
				return envRoot
			}
		}
	}

	// Last resort: parse the shell integration cache file.
	// The shell hook (gt rig detect --cache) writes entries like:
	//   /path/to/repo:export GT_TOWN_ROOT="/path/to/town"; export GT_ROOT=...
	// We extract the GT_TOWN_ROOT value from the first valid entry.
	if root := townRootFromShellCache(); root != "" {
		return root
	}

	return ""
}

// townRootFromShellCache reads ~/.cache/gastown/rigs.cache and extracts
// the GT_TOWN_ROOT value from the first entry that points to a valid town.
func townRootFromShellCache() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	cachePath := filepath.Join(home, ".cache", "gastown", "rigs.cache")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return ""
	}

	// Deduplicate: collect unique town roots from cache entries.
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		// Format: <repo>:export GT_TOWN_ROOT="<town>"; ...
		const marker = `GT_TOWN_ROOT="`
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(marker):]
		end := strings.Index(rest, `"`)
		if end <= 0 {
			continue
		}
		candidate := rest[:end]
		if seen[candidate] {
			continue
		}
		seen[candidate] = true

		// Validate: must have mayor/town.json or mayor/ directory.
		if _, statErr := os.Stat(filepath.Join(candidate, workspace.PrimaryMarker)); statErr == nil {
			return candidate
		}
		if info, statErr := os.Stat(filepath.Join(candidate, workspace.SecondaryMarker)); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// toolEvent represents a parsed tool_started/tool_finished or compaction_started/compaction_finished
// event from the events JSONL file, used to populate CurrentTool and IsCompacting for non-Claude agents.
type toolEvent struct {
	Timestamp time.Time
	Actor     string // e.g., "gastown/crew/joe"
	Session   string // tmux session name (from payload.session)
	Tool      string // e.g., "Bash(git status)"
	EventType string // "tool_started" or "tool_finished"
}

// readRecentToolEvents reads the last N lines of the events JSONL file
// and extracts tool_started/tool_finished events from the last 15 seconds.
// This is called on each poll to provide tool execution info for non-Claude agents.
func (m *Model) readRecentToolEvents() {
	m.recentToolEvents = nil

	if m.townRoot == "" {
		return
	}

	eventsPath := filepath.Join(m.townRoot, ".events.jsonl")
	f, err := os.Open(eventsPath)
	if err != nil {
		return
	}
	defer f.Close()

	// Seek to near the end of the file — we only care about recent events.
	// Tool events use a 15s window, but compaction events use 10 minutes.
	// At ~2 events/s * ~160 bytes, 10 minutes ≈ 192KB. Use 256KB to be safe.
	const tailSize = 256 * 1024
	info, err := f.Stat()
	if err != nil {
		return
	}
	if info.Size() > tailSize {
		if _, err := f.Seek(-tailSize, 2); err != nil {
			return
		}
	}

	cutoff := time.Now().Add(-15 * time.Second)
	compactionCutoff := time.Now().Add(-10 * time.Minute) // compaction events need longer window
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Quick pre-filter: only parse lines containing relevant event types
		lineStr := string(line)
		if !strings.Contains(lineStr, "tool_started") && !strings.Contains(lineStr, "tool_finished") &&
			!strings.Contains(lineStr, "compaction_started") && !strings.Contains(lineStr, "compaction_finished") {
			continue
		}

		var evt struct {
			Timestamp string                 `json:"ts"`
			Type      string                 `json:"type"`
			Actor     string                 `json:"actor"`
			Payload   map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Type != "tool_started" && evt.Type != "tool_finished" &&
			evt.Type != "compaction_started" && evt.Type != "compaction_finished" {
			continue
		}

		ts, err := time.Parse(time.RFC3339, evt.Timestamp)
		if err != nil {
			continue
		}
		// Compaction events use a longer window — they're rare (one pair per
		// compaction cycle) and need to persist through the entire compaction
		// duration (30-90+ seconds). Tool events use the short 15s window
		// because they're rapid-fire.
		isCompactionEvent := evt.Type == "compaction_started" || evt.Type == "compaction_finished"
		if isCompactionEvent {
			if ts.Before(compactionCutoff) {
				continue
			}
		} else {
			if ts.Before(cutoff) {
				continue
			}
		}

		te := toolEvent{
			Timestamp: ts,
			Actor:     evt.Actor,
			EventType: evt.Type,
		}
		if evt.Payload != nil {
			if tool, ok := evt.Payload["tool"].(string); ok && tool != "unknown" {
				te.Tool = tool
			}
			if session, ok := evt.Payload["session"].(string); ok {
				te.Session = session
			}
		}
		m.recentToolEvents = append(m.recentToolEvents, te)
	}
}

// applyToolEvents populates CurrentTool and IsCompacting for non-Claude agents
// using plugin-emitted events. Matches events to agents by tmux session name
// (preferred) or actor name (fallback).
// This is the sole owner of CurrentTool for OpenCode agents — parsePaneContentOpenCode
// does not set it.
func (m *Model) applyToolEvents() {
	// Reset CurrentTool for all non-Claude agents first. If no recent event
	// confirms a tool is still running, it should show as cleared.
	for _, a := range m.agents {
		if !isClaudeAgent(a.AgentType) {
			a.CurrentTool = ""
		}
	}

	if len(m.recentToolEvents) == 0 {
		return
	}

	// Build index of agents that need event-based tool info (non-Claude only)
	needsEvents := make(map[string]*AgentLight)
	for _, a := range m.agents {
		if !isClaudeAgent(a.AgentType) {
			needsEvents[a.SessionName] = a
		}
	}
	if len(needsEvents) == 0 {
		return
	}

	// Process events in chronological order — last event for a session wins.
	for _, evt := range m.recentToolEvents {
		// Find the matching agent by session name or actor fallback
		var matched *AgentLight
		if evt.Session != "" {
			matched = needsEvents[evt.Session]
		}
		if matched == nil && evt.Actor != "" {
			parts := strings.Split(evt.Actor, "/")
			if len(parts) > 0 {
				lastPart := parts[len(parts)-1]
				for _, a := range needsEvents {
					if strings.Contains(a.SessionName, lastPart) {
						matched = a
						break
					}
				}
			}
		}
		if matched == nil {
			continue
		}

		switch evt.EventType {
		case "tool_started":
			matched.CurrentTool = evt.Tool
		case "tool_finished":
			matched.CurrentTool = ""
		case "compaction_started":
			matched.IsCompacting = true
			matched.PreCompactCtxPct = matched.ContextPercent
		case "compaction_finished":
			matched.IsCompacting = false
		}
	}
}

// Init initializes the model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		m.pollSessions(),
		tea.SetWindowTitle("GT Activity"),
		tea.EnableMouseAllMotion, // Enable mouse tracking
	)
}

// Message types
type (
	sessionsMsg struct {
		sessions []sessionInfo
	}
	pollMsg struct{}
)

type sessionInfo struct {
	name      string
	activity  int64
	created   int64    // unix timestamp when session was created
	paneLines []string // captured pane content for status extraction
}

// pollSessions queries tmux for all Gas Town session activity.
func (m *Model) pollSessions() tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}|#{window_activity}|#{session_created}")
		out, err := cmd.Output()
		if err != nil {
			return sessionsMsg{sessions: nil}
		}

		var sessions []sessionInfo
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 3)
			if len(parts) < 2 {
				continue
			}
			name := parts[0]
			// Only Gas Town sessions (uses prefix registry to match all rig prefixes)
			if !session.IsKnownSession(name) {
				continue
			}
			var ts int64
			if _, err := fmt.Sscanf(parts[1], "%d", &ts); err != nil || ts == 0 {
				continue
			}
			var created int64
			if len(parts) >= 3 {
				fmt.Sscanf(parts[2], "%d", &created)
			}
			sessions = append(sessions, sessionInfo{name: name, activity: ts, created: created})
		}

		// Capture pane content for all sessions in a single shell invocation.
		// This replaces N individual tmux capture-pane subprocesses with 1.
		if len(sessions) > 0 {
			paneMap := batchCapturePanes(sessions)
			for i := range sessions {
				if lines, ok := paneMap[sessions[i].name]; ok {
					sessions[i].paneLines = lines
				}
			}
		}

		return sessionsMsg{sessions: sessions}
	}
}

// pollTick fires on the configured interval to re-poll tmux.
func (m *Model) pollTick() tea.Cmd {
	return tea.Tick(m.pollInterval, func(t time.Time) tea.Msg {
		return pollMsg{}
	})
}

// batchCapturePanes captures pane content for all sessions in a single shell
// invocation, replacing N individual tmux capture-pane subprocesses with 1.
// Returns a map from session name to captured lines.
func batchCapturePanes(sessions []sessionInfo) map[string][]string {
	// Build a shell script that captures each pane with a delimiter.
	// Delimiter format: ===PANE:sessionName===
	var script strings.Builder
	for _, s := range sessions {
		// Session names are safe (alphanumeric + hyphens from our naming convention)
		fmt.Fprintf(&script, "echo '===PANE:%s==='\n", s.name)
		fmt.Fprintf(&script, "tmux capture-pane -t '%s' -p -S -10 2>/dev/null\n", s.name)
	}

	cmd := exec.Command("sh", "-c", script.String())
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	result := make(map[string][]string, len(sessions))
	lines := strings.Split(string(out), "\n")
	var currentSession string
	var currentLines []string

	for _, line := range lines {
		if strings.HasPrefix(line, "===PANE:") && strings.HasSuffix(line, "===") {
			// Flush previous session
			if currentSession != "" {
				result[currentSession] = currentLines
			}
			currentSession = line[8 : len(line)-3]
			currentLines = nil
		} else if currentSession != "" {
			currentLines = append(currentLines, line)
		}
	}
	// Flush last session
	if currentSession != "" {
		result[currentSession] = currentLines
	}

	return result
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

		// Double-click detection: two left-button presses on the same agent within 500ms.
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			clickedAgent := m.agentAtY(msg.Y)
			if clickedAgent != nil && clickedAgent == m.lastClickAgent &&
				time.Since(m.lastClickTime) < 500*time.Millisecond {
				// Double-click detected — launch terminal attached to this session
				m.lastClickAgent = nil // reset to avoid triple-click
				m.openTerminalWithTmuxAttach(clickedAgent.SessionName)
			} else {
				m.lastClickAgent = clickedAgent
				m.lastClickTime = time.Now()
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case sessionsMsg:
		m.updateAgents(msg.sessions)
		m.blinkOn = !m.blinkOn
		m.tickNum++
		return m, m.pollTick()

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
			// New agent — detect agent type from tmux environment (one-time read)
			agentType := detectAgentType(s.name)
			agent = &AgentLight{
				SessionName:    s.name,
				AgentType:      agentType,
				CurActivity:    s.activity,
				PrevActivity:   s.activity,
				LastChangeTime: now,
			}
			if s.created > 0 {
				agent.SessionCreated = time.Unix(s.created, 0)
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
			// Update created time if session was restarted (new created timestamp)
			if s.created > 0 {
				newCreated := time.Unix(s.created, 0)
				if !newCreated.Equal(agent.SessionCreated) {
					agent.SessionCreated = newCreated
				}
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

	// Apply plugin-emitted tool events for non-Claude agents.
	// This populates CurrentTool from events written by gastown.js plugin
	// hooks (tool.execute.before/after), sidestepping pane parsing.
	m.readRecentToolEvents()
	m.applyToolEvents()

	// Apply compaction override AFTER both pane-scraping and event processing.
	// IsCompacting may have been set by parsePaneContentOpenCode (pane-based)
	// or applyToolEvents (event-based). We apply the override here so that
	// event-based detection takes effect in the same cycle.
	for _, a := range m.agents {
		if a.IsCompacting {
			a.StatusText = "COMPACTING"
			a.CurrentTool = ""
			a.ContextPercent = 0
		}
	}

	// Poll beads DB for work assignments (slower cadence, guarded internally)
	m.pollBeadsWork()

	// Rebuild rig ordering
	m.rebuildRigOrder()
}

// parseSessionName extracts role/rig/name from a session name using the
// session package's ParseSessionName (which resolves rig-specific beads
// prefixes via the PrefixRegistry). Dog sessions need special handling
// since the session package has no dog role.
func parseSessionName(a *AgentLight) {
	name := a.SessionName

	// Dog sessions: <prefix>-dog-<name> (town-level workers dispatched by Deacon).
	// The session package doesn't have a dog role, so we handle them before
	// calling ParseSessionName (which would parse "gt-dog-alpha" as polecat
	// name "dog-alpha"). We check all registered prefixes, not just "gt-".
	registry := session.DefaultRegistry()
	for _, prefix := range registry.Prefixes() {
		dogMarker := prefix + "-dog-"
		if strings.HasPrefix(name, dogMarker) {
			a.Rig = "hq" // town-level agents shown alongside mayor/deacon
			a.Role = constants.RoleDog
			a.Name = strings.TrimPrefix(name, dogMarker)
			a.Icon = constants.EmojiDog
			return
		}
	}
	// Also check hq-dog- (the canonical prefix from SessionManager) and
	// gt-dog- (legacy/default prefix) in case the registry has no match.
	for _, fallback := range []string{"hq-dog-", "gt-dog-"} {
		if strings.HasPrefix(name, fallback) {
			a.Rig = "hq"
			a.Role = constants.RoleDog
			a.Name = strings.TrimPrefix(name, fallback)
			a.Icon = constants.EmojiDog
			return
		}
	}

	id, err := session.ParseSessionName(name)
	if err != nil {
		a.Name = name
		a.Icon = "❓"
		return
	}

	// Map session.AgentIdentity to AgentLight fields
	switch id.Role {
	case session.RoleMayor:
		a.Rig = "hq"
		a.Role = constants.RoleMayor
		a.Name = "Mayor"
		a.Icon = constants.EmojiMayor
	case session.RoleDeacon:
		a.Rig = "hq"
		if id.Name == "boot" {
			a.Role = constants.RoleDeacon
			a.Name = "Boot"
			a.Icon = constants.EmojiDeacon // 🐺 same as deacon
		} else {
			a.Role = constants.RoleDeacon
			a.Name = "Deacon"
			a.Icon = constants.EmojiDeacon
		}
	case session.RoleWitness:
		a.Rig = id.Rig
		a.Role = constants.RoleWitness
		a.Name = "witness"
		a.Icon = constants.EmojiWitness
	case session.RoleRefinery:
		a.Rig = id.Rig
		a.Role = constants.RoleRefinery
		a.Name = "refinery"
		a.Icon = constants.EmojiRefinery
	case session.RoleCrew:
		a.Rig = id.Rig
		a.Role = constants.RoleCrew
		a.Name = id.Name
		a.Icon = constants.EmojiCrew
	case session.RolePolecat:
		if id.Rig == "" && id.Name == "overseer" {
			// hq-overseer: the human operator session
			a.Rig = "hq"
			a.Role = constants.RolePolecat
			a.Name = "overseer"
			a.Icon = "👤"
		} else {
			a.Rig = id.Rig
			a.Role = constants.RolePolecat
			a.Name = id.Name
			a.Icon = constants.EmojiPolecat
		}
	default:
		a.Name = name
		a.Icon = "❓"
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
// Lines are ordered top-to-bottom (time flows downward). For Claude Code sessions,
// we strip UI chrome from the bottom, then scan upward from the most recent real
// content to find status signals. For OpenCode agents, we parse their distinctive
// TUI patterns (▣ working indicator, ✱ tools, context %). For other non-Claude
// agents, we use a generic parser.
func parsePaneContent(a *AgentLight, lines []string) {
	// Lazy agent type detection from pane content.
	// GT_AGENT is rarely set in tmux env — detect from TUI signatures instead.
	// Once detected (non-empty), the type is cached and never re-detected.
	if a.AgentType == "" {
		a.AgentType = detectAgentTypeFromPane(lines)
	}

	// Dispatch to agent-specific parser.
	switch a.AgentType {
	case "opencode":
		parsePaneContentOpenCode(a, lines)
	default:
		// Claude Code or unknown agents use the Claude parser.
		parsePaneContentClaude(a, lines)
	}
}

// parsePaneContentClaude is the pane parser for Claude Code sessions.
// Strips UI chrome from the bottom, then scans upward from the most recent real
// content to find status signals (✻ working indicator, ⏺ tool execution, etc.).
func parsePaneContentClaude(a *AgentLight, lines []string) {
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

// parsePaneContentOpenCode is the pane parser for OpenCode sessions.
// OpenCode's TUI has distinctive patterns visible in tmux capture-pane:
//
//	▣  Build · claude-opus-4.6 · 2m 17s    — ALWAYS present (static chrome, NOT a working signal)
//	✱ Grep "pattern" in pkg/...             — tool execution in flight (WORKING signal)
//	→ Read file.go [offset=1, limit=20]     — tool result (completed, not in-flight)
//	~ Preparing write...                    — pending operation (WORKING signal)
//	~ Writing command...                    — pending command execution (WORKING signal)
//	⠏ Sling Ruby analyzer...               — braille spinner with action (WORKING signal)
//	■■■■■■⬝⬝  esc interrupt                — bottom status bar (filled ■ = progress)
//	Build  Claude Opus 4.6 GitHub Copilot   — model info line (chrome)
//	┃ ... ╹▀▀▀                              — box-drawing chrome
//	40,140  31% ($0.00)                     — context/token info in header
//	[•] Fix the parser                      — todo item in-progress (sidebar, wide panes only)
//
// Tool panel patterns (visible in tmux capture-pane output):
//
//	┃  # Explore Task                       — completed tool panel (# = done)
//	┃  Audit security in API/auth (24 tc)   — task description inside panel
//	┃  └ Read file.go                       — sub-tool inside panel
//	┃  $ command args 2>&1                  — command inside completed panel
//	┃  ✓ Success message                    — output inside completed panel
//	┃  ctrl+x right view subagents          — chrome inside panel
//	⠃ Explore Task                          — active tool panel (braille spinner = running)
//	Audit code quality/comments (69 tc)     — task description (active, no ┃ frame)
//	└ Read file.go                          — sub-tool (active, no ┃ frame)
//	$ command args 2>&1                     — bare command = actively executing
//
// sidebarInfo holds extracted data from the OpenCode sidebar.
// This is populated by extractAndStripSidebar before the sidebar is discarded.
type sidebarInfo struct {
	contextPercent int      // remaining context (100 - N% used), 0 = unknown
	tokenCount     int      // total tokens used (from "N,NNN tokens" line), 0 = unknown
	inProgressTodo string   // text of [•] in-progress todo item (current step)
	pendingTodos   []string // text of [ ] pending todo items
}

// extractAndStripSidebar detects the right-hand sidebar that OpenCode renders
// on wide terminals (pane width > 120), extracts useful data from it, then
// removes it from the lines.
//
// The sidebar is ~42 columns wide and contains Todo items, Context info,
// Modified Files, LSP status, etc.
//
// Detection: scan for lines where a large whitespace gap (8+ spaces) is followed
// by known sidebar markers (Context, ▼ Todo, [✓], [•], [ ], • gopls, etc.).
// The leftmost such gap position across all detected lines gives us the sidebar
// column. All lines are then truncated at that column.
//
// Extraction (before truncation):
//   - [•] in-progress todo item → the current step the agent is working on
//   - [ ] pending todo items → upcoming steps
//   - N% used → context usage (inverted to remaining)
//
// This prevents sidebar content from contaminating the main-content parser
// and causing false tool/status matches.
func extractAndStripSidebar(lines []string) ([]string, sidebarInfo) {
	var info sidebarInfo

	if len(lines) == 0 {
		return lines, info
	}

	// Known sidebar-only markers. These never appear as main content in
	// the left panel at the positions where we'd see them after a big gap.
	sidebarMarkers := []string{
		"Context",
		"tokens",
		"% used",
		"spent",
		"▼ Todo",
		"▼ Modified Files",
		"LSP",
		"LSPs will activate",
		"• gopls",
		"• OpenCode",
		"[✓]",
		"[•]",
		"[ ]",
	}

	// Scan lines to find the sidebar column. We look for lines where:
	// 1. Total length > 100 (sidebar only appears on wide panes)
	// 2. There's a gap of 8+ spaces
	// 3. Text after the gap matches a sidebar marker
	//
	// Strategy: collect the position where each sidebar marker starts,
	// then use the minimum marker position as the sidebar column. Using
	// the marker position (not the gap start) prevents false truncation
	// when lines have short main content followed by long gaps — the
	// sidebar boundary is where the sidebar text begins, not where the
	// preceding whitespace begins.
	sidebarCol := 0
	for _, line := range lines {
		if len(line) < 100 {
			continue
		}
		// Find gaps of 8+ spaces and check what follows
		for i := 0; i < len(line)-10; i++ {
			if line[i] != ' ' {
				continue
			}
			// Count consecutive spaces
			j := i + 1
			for j < len(line) && line[j] == ' ' {
				j++
			}
			gapLen := j - i
			if gapLen < 8 || j >= len(line) {
				continue
			}
			// Check if text after gap matches a sidebar marker
			after := strings.TrimSpace(line[j:])
			for _, marker := range sidebarMarkers {
				if strings.HasPrefix(after, marker) {
					// Use the marker position (j) as the sidebar boundary,
					// with a small buffer for visual separation. Subtract 2
					// to trim the trailing whitespace before the marker.
					markerCol := j - 2
					if markerCol < 0 {
						markerCol = 0
					}
					if sidebarCol == 0 || markerCol < sidebarCol {
						sidebarCol = markerCol
					}
					break
				}
			}
		}
	}

	if sidebarCol == 0 {
		return lines, info // no sidebar detected
	}

	// Extract sidebar content before truncation.
	for _, line := range lines {
		if len(line) <= sidebarCol {
			continue
		}
		sidebarText := strings.TrimSpace(line[sidebarCol:])
		if sidebarText == "" {
			continue
		}

		// [•] in-progress todo — the current step the agent is on.
		// Only keep the first one (there should only be one, but be safe).
		if strings.HasPrefix(sidebarText, "[•]") {
			todoText := strings.TrimSpace(strings.TrimPrefix(sidebarText, "[•]"))
			if todoText != "" && info.inProgressTodo == "" {
				info.inProgressTodo = todoText
			}
		}

		// [ ] pending todo items — upcoming steps.
		if strings.HasPrefix(sidebarText, "[ ]") {
			todoText := strings.TrimSpace(strings.TrimPrefix(sidebarText, "[ ]"))
			if todoText != "" {
				info.pendingTodos = append(info.pendingTodos, todoText)
			}
		}

		// N% used — context usage.
		if pct := extractOpenCodeSidebarContextPercent(line); pct > 0 {
			info.contextPercent = pct
		}

		// N,NNN tokens — total token count from sidebar.
		if tc := extractTokenCount(sidebarText); tc > 0 {
			info.tokenCount = tc
		}
	}

	// Truncate all lines at the sidebar column and trim trailing whitespace.
	result := make([]string, len(lines))
	for i, line := range lines {
		if len(line) > sidebarCol {
			result[i] = strings.TrimRight(line[:sidebarCol], " ")
		} else {
			result[i] = line
		}
	}
	return result, info
}

// parsePaneContentOpenCode extracts status signals from an OpenCode TUI pane.
//
// Tool execution (CurrentTool) is handled by the event stream — the gastown.js
// plugin emits tool_started/tool_finished events via "gt top emit", and
// applyToolEvents() populates CurrentTool from those events AFTER this function
// runs. This function therefore does NOT set CurrentTool.
//
// What this function extracts (things the event stream can't provide):
//   - ContextPercent: from sidebar "N% used" or header "N% ($X.XX)"
//   - WaitingForHuman: permission dialogs (△ Permission required)
//   - RateLimited: rate limit text, [retrying in Xs attempt #N]
//   - HitLimit: usage cap text
//   - StatusText: streaming state, pending ops, mode/elapsed from ▣ line
func parsePaneContentOpenCode(a *AgentLight, lines []string) {
	a.StatusText = ""
	a.WaitingForHuman = false
	a.WaitingReason = ""
	a.RateLimited = false
	a.HitLimit = false
	a.LimitResetInfo = ""
	// CurrentTool is NOT reset here — it's owned by applyToolEvents().
	// ContextPercent, TokenCount, SessionLimitPct, SessionLimitReset persist (sticky).

	if len(lines) == 0 {
		return
	}

	// Extract useful data from the sidebar AND strip it before parsing.
	// On wide panes (>120 cols), OpenCode renders a ~42-column sidebar with
	// Todo items, Context info, Modified Files, etc. We extract:
	//   - contextPercent: "N% used" → remaining context
	//   - inProgressTodo: [•] item → the current step the agent is on
	var sidebar sidebarInfo
	lines, sidebar = extractAndStripSidebar(lines)

	if sidebar.contextPercent > 0 {
		a.ContextPercent = sidebar.contextPercent
	}
	if sidebar.tokenCount > 0 {
		a.TokenCount = sidebar.tokenCount
	}

	if len(lines) == 0 {
		return
	}

	// Signals we extract from the pane.
	var elapsedTime string        // from ▣ line (e.g., "Build · 2m 17s" or just "Build")
	var pendingOp string          // from ~ lines ("Preparing write...", etc.)
	var agentIsStreaming bool     // "esc interrupt" visible = actively generating
	var activeToolPanel string    // from braille spinner lines (bottom-most = most recent)
	var sawCompactionDivider bool // "───── Compaction ─────" divider in THIS capture
	// lastHeaderIsCompaction tracks whether the BOTTOM-MOST ▣ header in the
	// pane is a Compaction header. We use only the last header because the pane
	// contains scrollback — old "▣  Build" headers from previous messages would
	// falsely clear IsCompacting if we checked all of them.
	var lastHeaderIsCompaction bool
	var sawAnyHeader bool

	// sawBillingError tracks whether we've seen a billing/payment error.
	// Unlike permission dialogs (which are active UI elements that disappear
	// when resolved), billing errors are one-shot messages in scrollback.
	// If real work appears BELOW the error (tools, streaming, pending ops),
	// the agent has recovered and we clear the flag.
	var sawBillingError bool

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		// ── "esc interrupt" means the agent is actively streaming/working ──
		// Detect BEFORE the chrome filter since isOpenCodeChromeLine skips it.
		if strings.Contains(trimmed, "esc interrupt") {
			agentIsStreaming = true
		}

		// Skip empty lines and pure box-drawing chrome.
		if trimmed == "" || isOpenCodeChromeLine(trimmed) {
			continue
		}

		// ── ▣ line: mode name and optional elapsed time ──
		// We track only the LAST (bottom-most) header because the pane contains
		// scrollback from previous messages. Old "▣  Build" headers would falsely
		// clear IsCompacting if we OR'd all headers together.
		if strings.HasPrefix(trimmed, "▣") {
			elapsedTime = extractOpenCodeElapsedTime(trimmed)
			sawAnyHeader = true
			if strings.Contains(trimmed, "Compaction") {
				// "▣  Compaction · claude-opus-4.6 · 9.7s" (3+ segments = has elapsed)
				// means compaction FINISHED — the ▣ header only renders elapsed time
				// for completed messages. Treat as non-compaction (it's done).
				// Count " · " separators to distinguish finished (2+) from active (1).
				sepCount := strings.Count(trimmed, " · ")
				lastHeaderIsCompaction = sepCount < 2
			} else {
				lastHeaderIsCompaction = false
			}
		}

		// ── Compaction divider: "───── Compaction ─────" ──
		// OpenCode renders a top-border box with centered " Compaction " title.
		// Unlike ▣ headers, the divider is unambiguous — it only appears for compaction.
		if strings.Contains(trimmed, "Compaction") && strings.ContainsRune(trimmed, '─') {
			sawCompactionDivider = true
		}

		// ── Pending operation: "~ Preparing write..." / "~ Writing command..." ──
		if strings.HasPrefix(trimmed, "~") {
			rest := strings.TrimSpace(trimmed[1:])
			if rest != "" {
				// "Updating todos..." is transient noise — only use as fallback
				if strings.Contains(strings.ToLower(rest), "updating todo") {
					if pendingOp == "" {
						pendingOp = rest
					}
				} else {
					pendingOp = rest
				}
				// Pending ops mean the agent is working — clear stale billing error
				if sawBillingError {
					sawBillingError = false
					a.WaitingForHuman = false
					a.WaitingReason = ""
				}
			}
		}

		// ── Context/token info in header line: "40,140  31% ($0.00)" ──
		if pct := extractOpenCodeContextPercent(trimmed); pct > 0 {
			a.ContextPercent = pct
		}
		if tc := extractOpenCodeHeaderTokenCount(trimmed); tc > 0 {
			a.TokenCount = tc
		}

		// ── Permission dialogs ──
		// Inside ┃ frames: "△ Permission required" or "Allow once"
		if strings.HasPrefix(trimmed, "┃") {
			inner := strings.TrimSpace(strings.TrimPrefix(trimmed, "┃"))
			if strings.Contains(inner, "Permission required") ||
				strings.Contains(inner, "Allow once") {
				a.WaitingForHuman = true
				a.WaitingReason = "permission"
			}
		}
		// Bare (outside ┃ frames): "△ Permission required"
		if strings.HasPrefix(trimmed, "△") && strings.Contains(trimmed, "Permission") {
			a.WaitingForHuman = true
			a.WaitingReason = "permission"
		}
		if strings.Contains(trimmed, "Allow once") && strings.Contains(trimmed, "Allow always") {
			a.WaitingForHuman = true
			a.WaitingReason = "permission"
		}

		// ── Billing / payment errors → needs human intervention ──
		// CreditsError from OpenCode API: agent cannot recover, human must add payment method.
		// e.g. Unauthorized: {"type":"error","error":{"type":"CreditsError","message":"No payment method..."}}
		// Unlike permission dialogs, these are one-shot messages in scrollback.
		// If real work appears below this line, the agent recovered and we clear it.
		if strings.Contains(lower, "creditserror") || strings.Contains(lower, "no payment method") {
			sawBillingError = true
			a.WaitingForHuman = true
			a.WaitingReason = "needs payment method"
			a.HitLimit = false // not a transient limit — human action required
		}

		// ── Real work signals clear stale billing errors ──
		// Since time flows top→bottom, any tool execution or agent output
		// BELOW the error means the agent recovered. We check for:
		//   - Braille spinner characters (⠋⠙⠹ etc.) = tool actively running
		//   - Completed tool frames (┃ # ...) = tool finished after error
		//   - ✱ tool markers = tool execution
		if sawBillingError {
			const brailleChars = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷"
			firstRune, _ := decodeFirstRune(trimmed)
			isWork := strings.ContainsRune(brailleChars, firstRune) ||
				strings.HasPrefix(trimmed, "✱") ||
				(strings.HasPrefix(trimmed, "┃") && strings.Contains(trimmed, "#"))
			if isWork {
				sawBillingError = false
				a.WaitingForHuman = false
				a.WaitingReason = ""
			}
		}

		// ── Rate limit / usage limit ──
		if strings.Contains(lower, "rate limit") && (strings.Contains(lower, "retry") || strings.Contains(lower, "resets") || strings.Contains(lower, "exceeded")) {
			a.RateLimited = true
		}
		if strings.Contains(lower, "hit your limit") || strings.Contains(lower, "credit balance too low") ||
			strings.Contains(lower, "quota exceeded") {
			a.HitLimit = true
			a.LimitResetInfo = extractLimitResetInfo(line)
		}
		// OpenCode retry pattern: "[retrying in Xs attempt #N]"
		if strings.Contains(lower, "retrying in") && strings.Contains(lower, "attempt") {
			a.RateLimited = true
		}
	}

	// ── Scan bottom-to-top for active tool panels ──
	// The main content area scrolls newest-at-bottom, so the bottom-most
	// braille spinner line is the most recent active tool. Active tool panels
	// look like:
	//   ⠃ Explore Task                    — braille spinner = running
	//   Explore repo structure (37 tc)    — task description
	//     └ Read                          — sub-tool
	// Completed panels use ┃ frames with # headers — we skip those.
	activeToolPanel = extractActiveToolPanel(lines)

	// If the agent is actively streaming or has an active tool panel,
	// any stale billing error from scrollback is superseded — agent recovered.
	if sawBillingError && (agentIsStreaming || activeToolPanel != "") {
		a.WaitingForHuman = false
		a.WaitingReason = ""
	}

	// ── Build StatusText ──
	// StatusText is supplemental context shown alongside the event-derived
	// CurrentTool. Priority:
	//   1. Pending operation (~) — "Preparing write...", etc.
	//   2. Active tool panel (braille spinner) — what tool is running right now
	//   3. Streaming + sidebar todo or elapsed — generating prose
	//   4. Sidebar [•] in-progress todo — what step the agent is on
	//   5. Mode/elapsed from ▣ — "Build · 2m 17s" or just "Build"
	if pendingOp != "" {
		a.StatusText = pendingOp
	} else if activeToolPanel != "" {
		a.StatusText = activeToolPanel
	} else if agentIsStreaming {
		if sidebar.inProgressTodo != "" {
			a.StatusText = sidebar.inProgressTodo
		} else if a.PrevStatusText != "" {
			// Agent is streaming (generating text) — keep the last meaningful
			// status with "..." to show it's still working on that, rather than
			// clobbering it with the uninformative "streaming".
			s := a.PrevStatusText
			s = strings.TrimSuffix(s, "...")
			a.StatusText = s + "..."
		} else if elapsedTime != "" {
			a.StatusText = elapsedTime
		}
		// else: StatusText stays "" — no previous context to carry forward
	} else if sidebar.inProgressTodo != "" {
		a.StatusText = sidebar.inProgressTodo
	} else if elapsedTime != "" {
		a.StatusText = elapsedTime
	}

	// Compaction detection from pane-scraping. Two signals:
	//   - Compaction divider ("───── Compaction ─────") — unambiguous in-progress signal
	//   - Bottom-most ▣ header says "Compaction" — appears after compaction finishes
	// We use only the LAST ▣ header because old "▣  Build" headers from previous
	// messages linger in scrollback and would falsely clear IsCompacting.
	//
	// Note: applyToolEvents() runs AFTER this function and can also set/clear
	// IsCompacting from plugin events. Event-based detection is more reliable
	// during compaction (pane shows no distinguishing signals while streaming
	// the compaction summary). Pane-scraping only catches it after compaction
	// finishes (when the ▣ Compaction header appears).
	sawCompactionInPane := sawCompactionDivider || (sawAnyHeader && lastHeaderIsCompaction)
	sawNonCompactionHeader := sawAnyHeader && !lastHeaderIsCompaction

	if sawCompactionInPane && !a.IsCompacting {
		a.IsCompacting = true
		a.PreCompactCtxPct = a.ContextPercent // snapshot before we zero it
	}
	// Only clear from pane-scraping if the BOTTOM-MOST header is non-Compaction.
	// This prevents old headers in scrollback from falsely clearing the state.
	if sawNonCompactionHeader && !sawCompactionDivider {
		a.IsCompacting = false
	}
	// Context% drop: compaction prunes old context, so usage drops dramatically.
	// If we see context% reappear at well below the pre-compaction level, it's over.
	if a.IsCompacting && a.PreCompactCtxPct > 0 && a.ContextPercent > 0 &&
		a.ContextPercent < a.PreCompactCtxPct/2 {
		a.IsCompacting = false
	}

	// NOTE: The compaction override (StatusText = "COMPACTING", ContextPercent = 0)
	// is applied in the main poll loop AFTER applyToolEvents(), not here.
	// This ensures event-based IsCompacting (set by applyToolEvents) takes effect
	// in the same cycle rather than being delayed by one poll.

	// HitLimit is terminal — clear stale status
	if a.HitLimit {
		a.StatusText = ""
	}
	// WaitingForHuman (billing) is terminal — clear stale tool/status
	if a.WaitingForHuman && a.WaitingReason == "needs payment method" {
		a.StatusText = ""
		a.CurrentTool = ""
	}

	// Save for next cycle — lets streaming carry forward the last meaningful status.
	if a.StatusText != "" && !strings.HasSuffix(a.StatusText, "...") {
		a.PrevStatusText = a.StatusText
	}
}

// isOpenCodeChromeLine returns true if the line is OpenCode TUI chrome that
// should be ignored when extracting status. These are always-present UI
// elements that carry no status information.
func isOpenCodeChromeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	// Pure box-drawing: lines that are only ┃, ╹, ▀, spaces
	if isBoxDrawingOnly(trimmed) {
		return true
	}
	// Bottom status bar: "esc interrupt" with optional progress dots
	if strings.Contains(trimmed, "esc interrupt") {
		return true
	}
	// Bottom bar: "ctrl+t variants  tab agents  ctrl+p commands"
	if strings.Contains(trimmed, "ctrl+p commands") {
		return true
	}
	// Model info line: "Build  Claude Opus 4.6 GitHub Copilot"
	if strings.HasPrefix(trimmed, "Build ") && strings.Contains(trimmed, "Copilot") {
		return true
	}
	// Separator lines
	if isSeparatorLine(trimmed) {
		return true
	}
	return false
}

// isBoxDrawingOnly returns true if the string contains only box-drawing
// characters, spaces, and bar characters used in OpenCode's frame.
func isBoxDrawingOnly(s string) bool {
	for _, r := range s {
		switch r {
		case '┃', '╹', '▀', '│', '┌', '┐', '└', '┘', '─', '━',
			'═', '║', '╔', '╗', '╚', '╝', ' ', '\t':
			continue
		default:
			return false
		}
	}
	return true
}

// extractOpenCodeElapsedTime extracts the task name and elapsed time from an OpenCode ▣ line.
// The ▣ line is ALWAYS present (static chrome). "Build", "Plan", and "Compaction" are
// standard modes that are noise — we suppress them. Compaction state is tracked separately
// via IsCompacting. Other task names would be meaningful.
// Input:  "▣  Build · claude-opus-4.6 · 2m 17s"    → "" (Build is noise)
// Input:  "▣  Compaction · claude-opus-4.6 · 1m 6s" → "" (Compaction is noise)
// Input:  "▣  Build · claude-opus-4.6"               → "" (Build is noise)
func extractOpenCodeElapsedTime(line string) string {
	trimmed := strings.TrimSpace(line)
	idx := strings.Index(trimmed, "▣")
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(trimmed[idx+len("▣"):])
	if rest == "" {
		return ""
	}

	// Split on " · " to get segments: [taskName, model, elapsed?]
	segments := strings.Split(rest, " · ")
	taskName := strings.TrimSpace(segments[0])

	// "Build", "Plan", and "Compaction" are standard OpenCode modes — not useful
	// as status. Suppress them so bead info can be the primary content.
	// Compaction state is tracked separately via IsCompacting.
	if taskName == "Build" || taskName == "Plan" || taskName == "Compaction" {
		return ""
	}

	if len(segments) < 3 {
		// No elapsed time segment — return mode name as fallback
		if taskName != "" {
			return taskName
		}
		return ""
	}

	// Last segment should look like a duration: "2m 17s", "45s", "1h 3m"
	lastSeg := strings.TrimSpace(segments[len(segments)-1])
	if !looksLikeDuration(lastSeg) {
		// Not a duration — return mode name as fallback
		if taskName != "" {
			return taskName
		}
		return ""
	}

	// Return "TaskName · elapsed" (skip the model name, it's noise)
	if taskName != "" {
		return taskName + " · " + lastSeg
	}
	return lastSeg
}

// looksLikeDuration returns true if the string resembles a time duration
// like "2m 17s", "45s", "1h 3m 12s", etc.
func looksLikeDuration(s string) bool {
	if len(s) == 0 || len(s) > 20 {
		return false
	}
	hasTimeUnit := false
	for _, r := range s {
		if r == 's' || r == 'm' || r == 'h' {
			hasTimeUnit = true
		} else if r >= '0' && r <= '9' {
			// digits are fine
		} else if r == ' ' {
			// spaces between parts are fine
		} else {
			return false // unexpected character
		}
	}
	return hasTimeUnit
}

// extractActiveToolPanel scans OpenCode's main content bottom-to-top for the
// most recent active tool panel. Active panels are identified by braille spinner
// characters (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷) at the start of a line.
//
// Examples of active tool panels in capture-pane output:
//
//	⠃ Explore Task                    — spinner + tool name (ACTIVE)
//	Explore repo structure (37 tc)    — task description (follows spinner line)
//	  └ Read                          — sub-tool being executed
//
//	⠏ Bash                            — spinner + tool name
//	$ git log --oneline -5            — command being run
//
// Completed panels have ┃ frames and # headers — those are NOT active:
//
//	┃  # Explore Task                 — completed (note the # prefix)
//
// Returns the tool name from the spinner line (e.g., "Explore Task", "Bash"),
// or "" if no active tool panel is found.
func extractActiveToolPanel(lines []string) string {
	const brailleSpinners = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏⣾⣽⣻⢿⡿⣟⣯⣷"

	// Scan bottom-to-top: the main content scrolls with newest at bottom,
	// so the first spinner we find from the bottom is the most recent tool.
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}

		// Check if the line starts with a braille spinner character.
		// We decode the first rune to check against our set.
		firstRune, size := decodeFirstRune(trimmed)
		if size == 0 {
			continue
		}
		if !strings.ContainsRune(brailleSpinners, firstRune) {
			continue
		}

		// Found a spinner line. Extract the tool name after it.
		rest := strings.TrimSpace(trimmed[size:])
		if rest == "" {
			return "working"
		}

		// Strip parenthetical stats if present: "Explore Task (37 toolcalls)" → "Explore Task"
		if idx := strings.LastIndex(rest, "("); idx > 0 {
			candidate := strings.TrimSpace(rest[:idx])
			if candidate != "" {
				return candidate
			}
		}
		return rest
	}
	return ""
}

// decodeFirstRune returns the first rune and its byte length from a string.
// Returns (0, 0) if the string is empty.
func decodeFirstRune(s string) (rune, int) {
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

// extractOpenCodeContextPercent extracts context usage percent from OpenCode's header.
// The header line looks like: "# Begin work on hook...  40,140  31% ($0.00)"
// or just the right-aligned portion: "40,140  31% ($0.00)"
// We want to extract 31 and compute context remaining (100 - 31 = 69%).
func extractOpenCodeContextPercent(line string) int {
	// Look for pattern: digits followed by "% (" — this is the usage percent
	// e.g., "31% ($0.00)" or "45% ($0.12)"
	trimmed := strings.TrimSpace(line)
	for i := 0; i < len(trimmed)-3; i++ {
		if trimmed[i] >= '0' && trimmed[i] <= '9' && i+1 < len(trimmed) {
			// Find the end of the number
			j := i + 1
			for j < len(trimmed) && trimmed[j] >= '0' && trimmed[j] <= '9' {
				j++
			}
			// Check if followed by "% ("
			if j+2 < len(trimmed) && trimmed[j] == '%' && trimmed[j+1] == ' ' && trimmed[j+2] == '(' {
				numStr := trimmed[i:j]
				var pct int
				if _, err := fmt.Sscanf(numStr, "%d", &pct); err == nil && pct >= 0 && pct <= 100 {
					// OpenCode shows usage %, gt top shows remaining %
					return 100 - pct
				}
			}
		}
	}
	return 0
}

// extractOpenCodeSidebarContextPercent extracts context usage from the sidebar.
// The sidebar shows "N% used" (e.g., "42% used") as a standalone line in the
// right panel. This appears on wide panes (>120 cols) where the sidebar is visible.
// We need to extract this BEFORE stripping the sidebar.
// Returns remaining percent (100 - usage), or 0 if not found.
func extractOpenCodeSidebarContextPercent(line string) int {
	trimmed := strings.TrimSpace(line)
	// The sidebar "N% used" appears after a large whitespace gap.
	// Look for the pattern anywhere in the line: "N% used"
	idx := strings.Index(trimmed, "% used")
	if idx < 0 {
		return 0
	}
	// Walk backward from "% used" to find the number
	numEnd := idx
	numStart := numEnd - 1
	for numStart >= 0 && trimmed[numStart] >= '0' && trimmed[numStart] <= '9' {
		numStart--
	}
	numStart++ // point to first digit
	if numStart >= numEnd {
		return 0
	}
	numStr := trimmed[numStart:numEnd]
	var pct int
	if _, err := fmt.Sscanf(numStr, "%d", &pct); err == nil && pct >= 0 && pct <= 100 {
		return 100 - pct
	}
	return 0
}

// extractTokenCount extracts a token count from a sidebar line like "40,140 tokens".
// Returns the integer token count (e.g. 40140), or 0 if not found.
func extractTokenCount(text string) int {
	idx := strings.Index(text, "tokens")
	if idx < 0 {
		return 0
	}
	// Walk backward from "tokens" to find the number (may contain commas)
	numEnd := idx
	for numEnd > 0 && text[numEnd-1] == ' ' {
		numEnd--
	}
	if numEnd == 0 {
		return 0
	}
	numStart := numEnd - 1
	for numStart > 0 && (text[numStart-1] >= '0' && text[numStart-1] <= '9' || text[numStart-1] == ',') {
		numStart--
	}
	if numStart >= numEnd {
		return 0
	}
	numStr := strings.ReplaceAll(text[numStart:numEnd], ",", "")
	var count int
	if _, err := fmt.Sscanf(numStr, "%d", &count); err == nil && count > 0 {
		return count
	}
	return 0
}

// extractOpenCodeHeaderTokenCount extracts token count from the OpenCode header line.
// The header looks like: "40,140  31% ($0.00)" — we want the "40,140" part.
// The token count is a comma-separated number that precedes "  N% (" with whitespace.
func extractOpenCodeHeaderTokenCount(line string) int {
	trimmed := strings.TrimSpace(line)
	// Find the "% (" pattern (same as extractOpenCodeContextPercent uses)
	for i := 0; i < len(trimmed)-3; i++ {
		if trimmed[i] >= '0' && trimmed[i] <= '9' {
			j := i + 1
			for j < len(trimmed) && trimmed[j] >= '0' && trimmed[j] <= '9' {
				j++
			}
			if j+2 < len(trimmed) && trimmed[j] == '%' && trimmed[j+1] == ' ' && trimmed[j+2] == '(' {
				// Found the percent. Now look backward from 'i' for the token count.
				// There should be whitespace, then a comma-separated number.
				pos := i - 1
				for pos >= 0 && trimmed[pos] == ' ' {
					pos--
				}
				if pos < 0 {
					return 0
				}
				// Walk backward through digits and commas
				numEnd := pos + 1
				for pos >= 0 && (trimmed[pos] >= '0' && trimmed[pos] <= '9' || trimmed[pos] == ',') {
					pos--
				}
				numStart := pos + 1
				if numStart >= numEnd {
					return 0
				}
				numStr := strings.ReplaceAll(trimmed[numStart:numEnd], ",", "")
				var count int
				if _, err := fmt.Sscanf(numStr, "%d", &count); err == nil && count > 0 {
					return count
				}
				return 0
			}
		}
	}
	return 0
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

	// Light safety trim - real truncation happens in renderLight based on terminal width
	return truncateStatus(rest)
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

// agentAtY returns the agent at the given Y coordinate, or nil.
func (m *Model) agentAtY(y int) *AgentLight {
	for _, a := range m.agents {
		if a.renderY > 0 && y >= a.renderY && y < a.renderY+a.renderHeight {
			return a
		}
	}
	return nil
}

// openTerminalWithTmuxAttach launches a new terminal window/tab running
// "tmux attach -t <session>". On macOS, it tries iTerm2 first (AppleScript),
// then falls back to Terminal.app. The command is run in the background so
// it doesn't block the TUI.
func (m *Model) openTerminalWithTmuxAttach(sessionName string) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		m.flashMessage = "tmux not found"
		m.flashTime = time.Now()
		return
	}

	attachCmd := fmt.Sprintf("%s attach -t %s", tmuxPath, sessionName)

	// Try iTerm2 first (very common on macOS for dev)
	// Request 192x60 so the agent TUI (especially OpenCode's sidebar) renders fully.
	iterm := exec.Command("osascript", "-e", fmt.Sprintf(
		`tell application "iTerm2"
			set newWindow to (create window with default profile command "%s")
			tell current session of current window
				set columns to 180
				set rows to 50
			end tell
		end tell`, attachCmd))
	if err := iterm.Start(); err == nil {
		m.flashMessage = "Opened iTerm2 → " + sessionName
		m.flashTime = time.Now()
		return
	}

	// Fallback: macOS Terminal.app
	terminal := exec.Command("osascript", "-e", fmt.Sprintf(
		`tell application "Terminal"
			do script "%s"
			activate
			-- resize the front window to 180 columns x 60 rows
			set number of columns of front window to 180
			set number of rows of front window to 50
		end tell`, attachCmd))
	if err := terminal.Start(); err == nil {
		m.flashMessage = "Opened Terminal → " + sessionName
		m.flashTime = time.Now()
		return
	}

	// Last resort: try generic x-terminal-emulator (Linux)
	generic := exec.Command("x-terminal-emulator", "-e", attachCmd)
	if err := generic.Start(); err == nil {
		m.flashMessage = "Opened terminal → " + sessionName
		m.flashTime = time.Now()
		return
	}

	m.flashMessage = "Could not open terminal"
	m.flashTime = time.Now()
}

// beadsPollInterval controls how often we query the beads DB.
// Slower than the 1s tmux poll since DB queries are heavier.
const beadsPollInterval = 5 * time.Second

// agentBeadMatch holds the parsed info for an agent bead, matched to its AgentLight.
type agentBeadMatch struct {
	agent      *AgentLight
	rig        string
	role       string
	fields     *beads.AgentFields
	hookBeadID string // from HookBead slot or parsed fields
	activeMR   string // from fields.ActiveMR (refinery only)
}

// pollBeadsWork queries the beads DB for all agents' current work assignments.
// Called on a slower cadence than the tmux session poll to avoid hammering the DB.
//
// Performance: uses batched ShowMultiple() to fetch all hook beads and ActiveMR
// beads in a single bd subprocess per rig, instead of N individual Show() calls.
func (m *Model) pollBeadsWork() {
	if m.townRoot == "" {
		return
	}

	now := time.Now()
	if now.Sub(m.lastBeadsPoll) < beadsPollInterval {
		return
	}
	m.lastBeadsPoll = now

	// Discover rig beads directories (cached after first discovery)
	if m.rigBeadsDirs == nil {
		m.rigBeadsDirs = m.discoverRigBeadsDirs()
	}

	// Build lookup from session name to agent for fast matching
	agentBySession := make(map[string]*AgentLight, len(m.agents))
	for _, a := range m.agents {
		agentBySession[a.SessionName] = a
	}

	// Query each rig's beads DB for agent beads
	for _, beadsDir := range m.rigBeadsDirs {
		b := beads.New(beadsDir)
		agentBeads, err := b.ListAgentBeads()
		if err != nil {
			continue
		}

		// ── Pass 1: Parse agent beads, collect IDs to batch-fetch ──
		var matches []agentBeadMatch
		beadIDsToFetch := make(map[string]bool) // deduplicated set of bead IDs

		for id, issue := range agentBeads {
			rig, role, name, ok := beads.ParseAgentBeadID(id)
			if !ok {
				continue
			}

			// Find the matching AgentLight by deriving the session name
			sessionName := deriveSessionNameForBeads(rig, role, name)
			agent, found := agentBySession[sessionName]
			if !found {
				for _, alt := range alternateSessionNames(rig, role, name) {
					if a, ok := agentBySession[alt]; ok {
						agent = a
						found = true
						break
					}
				}
			}
			if !found {
				continue
			}

			// Parse agent fields
			fields := beads.ParseAgentFields(issue.Description)
			if fields != nil {
				agent.AgentState = fields.AgentState
			}

			// Keep patrol summary warm for patrol agents (uses cache, see below)
			if patrolRoles[role] {
				if summary := m.fetchLastPatrolSummaryCached(b, rig, role); summary != "" {
					agent.LastPatrol = summary
				}
			}

			// Determine which bead ID to fetch
			hookBeadID := issue.HookBead
			if hookBeadID == "" && fields != nil {
				hookBeadID = fields.HookBead
			}

			match := agentBeadMatch{
				agent:      agent,
				rig:        rig,
				role:       role,
				fields:     fields,
				hookBeadID: hookBeadID,
			}

			// Refinery agents use ActiveMR instead of HookBead
			if hookBeadID == "" && role == "refinery" && fields != nil && fields.ActiveMR != "" {
				match.activeMR = fields.ActiveMR
				beadIDsToFetch[fields.ActiveMR] = true
			} else if hookBeadID != "" {
				beadIDsToFetch[hookBeadID] = true
			}

			matches = append(matches, match)
		}

		// ── Batch fetch all needed beads in one subprocess ──
		var fetchedBeads map[string]*beads.Issue
		if len(beadIDsToFetch) > 0 {
			ids := make([]string, 0, len(beadIDsToFetch))
			for id := range beadIDsToFetch {
				ids = append(ids, id)
			}
			fetchedBeads, _ = b.ShowMultiple(ids)
		}
		if fetchedBeads == nil {
			fetchedBeads = make(map[string]*beads.Issue)
		}

		// ── Pass 2: Apply fetched bead data to agents ──
		// Also collect molecule IDs that need children queries.
		type molQuery struct {
			agent      *AgentLight
			moleculeID string
		}
		var molQueries []molQuery

		for _, match := range matches {
			agent := match.agent

			if match.activeMR != "" {
				// Refinery with ActiveMR
				agent.WorkBeadID = match.activeMR
				mrBead := fetchedBeads[match.activeMR]
				if mrBead != nil {
					agent.WorkBeadTitle = mrBead.Title
					mrFields := beads.ParseMRFields(mrBead)
					if mrFields != nil && mrFields.Branch != "" && mrBead.Title == "" {
						agent.WorkBeadTitle = "MR: " + mrFields.Branch
					}
				} else {
					agent.WorkBeadTitle = ""
				}
				agent.FormulaName = ""
				agent.StepCurrent = ""
				agent.StepsDone = 0
				agent.StepsTotal = 0
				continue
			}

			if match.hookBeadID == "" {
				agent.WorkBeadID = ""
				agent.WorkBeadTitle = ""
				agent.FormulaName = ""
				agent.StepCurrent = ""
				agent.StepsDone = 0
				agent.StepsTotal = 0
				continue
			}

			hookBead := fetchedBeads[match.hookBeadID]
			if hookBead == nil {
				agent.WorkBeadID = match.hookBeadID
				agent.WorkBeadTitle = ""
				agent.FormulaName = ""
				agent.StepCurrent = ""
				agent.StepsDone = 0
				agent.StepsTotal = 0
				continue
			}

			agent.WorkBeadID = match.hookBeadID
			agent.WorkBeadTitle = hookBead.Title

			attachment := beads.ParseAttachmentFields(hookBead)
			if attachment == nil {
				agent.FormulaName = ""
				agent.StepCurrent = ""
				agent.StepsDone = 0
				agent.StepsTotal = 0
				continue
			}

			agent.FormulaName = attachment.AttachedFormula
			if attachment.AttachedMolecule != "" {
				molQueries = append(molQueries, molQuery{agent: agent, moleculeID: attachment.AttachedMolecule})
			} else {
				agent.StepCurrent = ""
				agent.StepsDone = 0
				agent.StepsTotal = 0
			}
		}

		// ── Pass 3: Fetch molecule progress ──
		// Each molecule needs a List(parent=moleculeID) call. These can't be
		// batched into one bd call, but there are typically only 0-3 molecules
		// active at a time per rig.
		for _, mq := range molQueries {
			m.fetchMoleculeProgress(b, mq.agent, mq.moleculeID)
		}
	}

	// ── Pass 4: Populate patrol summaries for hq agents without agent beads ──
	// The deacon is a town-level agent whose patrol wisps live in the hq beads
	// DB, but it has no agent bead (gt:agent label). Without this pass, its
	// LastPatrol would never get populated since the main loop above only
	// processes agents discovered through ListAgentBeads().
	if hqBeadsDir, ok := m.rigBeadsDirs["hq"]; ok {
		hqBeads := beads.New(hqBeadsDir)
		for _, a := range m.agents {
			if a.Rig != "hq" || !patrolRoles[a.Role] {
				continue
			}
			// Skip if already populated by the agent-bead loop above
			if a.LastPatrol != "" {
				continue
			}
			if summary := m.fetchLastPatrolSummaryCached(hqBeads, a.Rig, a.Role); summary != "" {
				a.LastPatrol = summary
			}
		}
	}
}

// fetchMoleculeProgress queries step progress for an attached molecule.
// Uses only List(parent=moleculeID) to get all children, then derives the
// current step from status and BlockedByCount — avoiding a separate
// ReadyForMol subprocess call.
func (m *Model) fetchMoleculeProgress(b *beads.Beads, agent *AgentLight, moleculeID string) {
	children, err := b.List(beads.ListOptions{
		Parent:   moleculeID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil || len(children) == 0 {
		return
	}

	agent.StepsTotal = len(children)
	agent.StepsDone = 0
	agent.StepCurrent = ""

	var currentStep *beads.Issue
	var firstReady *beads.Issue
	for _, child := range children {
		switch child.Status {
		case "closed":
			agent.StepsDone++
		case "in_progress", beads.StatusPinned, beads.StatusHooked:
			if currentStep == nil {
				currentStep = child
			}
		case "open":
			// A step is "ready" if it's open and not blocked by anything.
			// This replaces the separate ReadyForMol() subprocess call.
			if firstReady == nil && child.BlockedByCount == 0 {
				firstReady = child
			}
		}
	}

	if currentStep != nil {
		agent.StepCurrent = currentStep.Title
	} else if firstReady != nil {
		agent.StepCurrent = firstReady.Title
	}
}

// patrolRoles are agent roles that run patrol cycles and report summaries.
var patrolRoles = map[string]bool{
	"refinery": true,
	"witness":  true,
	"deacon":   true,
}

// patrolAssignee returns the beads assignee string for a patrol agent.
// Rig-scoped roles use "rig/role", global roles use just "role".
func patrolAssignee(rig, role string) string {
	switch role {
	case "deacon", "mayor":
		return role
	default:
		return rig + "/" + role
	}
}

// patrolCacheEntry holds a cached patrol summary with TTL.
type patrolCacheEntry struct {
	summary   string
	fetchedAt time.Time
}

// patrolCacheTTL controls how long patrol summaries are cached.
// Patrol summaries change every few minutes, not every 5s beads poll.
const patrolCacheTTL = 30 * time.Second

// fetchLastPatrolSummaryCached returns a cached patrol summary if fresh,
// otherwise queries the beads DB and caches the result.
func (m *Model) fetchLastPatrolSummaryCached(b *beads.Beads, rig, role string) string {
	key := rig + "/" + role
	now := time.Now()

	if m.patrolCache != nil {
		if entry, ok := m.patrolCache[key]; ok && now.Sub(entry.fetchedAt) < patrolCacheTTL {
			return entry.summary
		}
	}

	summary := fetchLastPatrolSummary(b, rig, role)

	if m.patrolCache == nil {
		m.patrolCache = make(map[string]patrolCacheEntry)
	}
	m.patrolCache[key] = patrolCacheEntry{summary: summary, fetchedAt: now}
	return summary
}

// fetchLastPatrolSummary queries the beads DB for the most recently closed
// patrol wisp and returns the cleaned-up summary text.
// Returns "" if no patrol summary is found.
func fetchLastPatrolSummary(b *beads.Beads, rig, role string) string {
	assignee := patrolAssignee(rig, role)
	closed, err := b.List(beads.ListOptions{
		Status:       "closed",
		Assignee:     assignee,
		Priority:     -1,
		Limit:        5,
		IncludeInfra: true, // patrol wisps are ephemeral beads
	})
	if err != nil || len(closed) == 0 {
		return ""
	}

	// Find most recent patrol report (description starts with "Patrol report: ")
	for _, issue := range closed {
		if !strings.HasPrefix(issue.Description, "Patrol report: ") {
			continue
		}
		summary := strings.TrimPrefix(issue.Description, "Patrol report: ")
		return cleanPatrolSummary(summary)
	}
	return ""
}

// cleanPatrolSummary strips boilerplate from patrol summaries to surface
// the operationally useful content.
//
// Input:  "Cycle 1: Queue empty (12th consecutive empty cycle overall). Inbox clean. Session healthy. Looping."
// Output: "Queue empty. Inbox clean."
func cleanPatrolSummary(s string) string {
	// Strip multi-line step audit appended by gt patrol (e.g.,
	// "\nSteps: NOT REPORTED (?/26)" or "\nSteps: heartbeat OK | ...").
	// This appears after a newline in the patrol wisp description and
	// doesn't belong in the one-line status display.
	if idx := strings.Index(s, "\nSteps:"); idx >= 0 {
		s = s[:idx]
	}
	// Also handle "\n\nSteps:" (double newline before steps)
	if idx := strings.Index(s, "\n\nSteps:"); idx >= 0 {
		s = s[:idx]
	}

	// Strip common cycle/patrol prefixes:
	//   "Cycle N: ...", "Cycle clean: ...", "Patrol N: ..."
	if idx := strings.Index(s, ": "); idx >= 0 && idx < 20 {
		prefix := s[:idx]
		if strings.HasPrefix(prefix, "Cycle ") || strings.HasPrefix(prefix, "Patrol ") {
			s = s[idx+2:]
		}
	}

	// Remove parenthetical asides about consecutive cycles or overall counts
	// e.g., "(12th consecutive empty cycle overall)", "(3rd consecutive empty cycle)"
	for {
		start := strings.Index(s, "(")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ")")
		if end < 0 {
			break
		}
		inner := strings.ToLower(s[start+1 : start+end])
		if strings.Contains(inner, "consecutive") || strings.Contains(inner, "overall") {
			before := strings.TrimRight(s[:start], " ,")
			after := s[start+end+1:]
			s = before + after
		} else {
			break // don't strip other parentheticals
		}
	}

	// Strip trailing health chatter (order matters — longest first)
	for _, suffix := range []string{
		"Session fresh, looping.",
		"Session healthy. Looping.",
		"Session healthy.",
		"Looping.",
		"No changes.",
		"No mail.",
	} {
		s = strings.TrimSuffix(s, suffix)
	}

	// Strip "All idle polecats healthy" and similar trailing boilerplate
	for _, suffix := range []string{
		"All idle polecats healthy",
		"Deacon alive",
		"No timer gates, no swarm",
	} {
		s = strings.TrimSuffix(strings.TrimRight(s, " ."), suffix)
	}

	// Collapse any remaining newlines into spaces — patrol summaries
	// must be single-line for the agent status display.
	s = strings.ReplaceAll(s, "\n", " ")
	// Collapse multiple spaces from newline replacement
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}

	s = strings.TrimRight(s, " .")
	if s != "" {
		s += "."
	}
	return s
}

// discoverRigBeadsDirs finds the beads directory for each rig.
func (m *Model) discoverRigBeadsDirs() map[string]string {
	dirs := make(map[string]string)

	// HQ beads (town-level)
	hqBeads := filepath.Join(m.townRoot, ".beads")
	if _, err := os.Stat(hqBeads); err == nil {
		dirs["hq"] = m.townRoot
	}

	// Discover rigs from rigs.json
	rigsConfigPath := filepath.Join(m.townRoot, "mayor", "rigs.json")
	if rigsConfig, err := config.LoadRigsConfig(rigsConfigPath); err == nil {
		for rigName := range rigsConfig.Rigs {
			rigDir := findRigBeadsDir(m.townRoot, rigName)
			if rigDir != "" {
				dirs[rigName] = rigDir
			}
		}
		return dirs
	}

	// Fallback: scan directory
	entries, err := os.ReadDir(m.townRoot)
	if err != nil {
		return dirs
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "mayor" || name == "daemon" || name == "deacon" ||
			name == ".git" || name == "docs" || name[0] == '.' {
			continue
		}
		rigDir := findRigBeadsDir(m.townRoot, name)
		if rigDir != "" {
			dirs[name] = rigDir
		}
	}
	return dirs
}

// findRigBeadsDir returns the beads work directory for a rig.
// Returns empty string if no beads directory exists.
// A beads directory must have metadata.json to be usable (the dir
// might exist with only locks/audit files but no database config).
func findRigBeadsDir(townRoot, rigName string) string {
	// Prefer mayor/rig/.beads (canonical location) if it has metadata.json
	mayorBeads := filepath.Join(townRoot, rigName, "mayor", "rig", ".beads")
	if _, err := os.Stat(filepath.Join(mayorBeads, "metadata.json")); err == nil {
		return filepath.Dir(mayorBeads) // beads.New wants the parent of .beads
	}
	// Fall back to rig-root .beads
	rigBeads := filepath.Join(townRoot, rigName, ".beads")
	if _, err := os.Stat(filepath.Join(rigBeads, "metadata.json")); err == nil {
		return filepath.Dir(rigBeads)
	}
	return ""
}

// deriveSessionNameForBeads maps agent bead components to tmux session name.
// This reverses the parseSessionName logic using the session name functions.
func deriveSessionNameForBeads(rig, role, name string) string {
	registry := session.DefaultRegistry()

	switch role {
	case "mayor":
		return session.MayorSessionName()
	case "deacon":
		if name == "boot" {
			return "hq-boot"
		}
		return session.DeaconSessionName()
	case "dog":
		prefix := registry.PrefixForRig(rig)
		if prefix == "" {
			prefix = "gt"
		}
		return prefix + "-dog-" + name
	case "witness":
		return session.WitnessSessionName(registry.PrefixForRig(rig))
	case "refinery":
		return session.RefinerySessionName(registry.PrefixForRig(rig))
	case "crew":
		return session.CrewSessionName(registry.PrefixForRig(rig), name)
	case "polecat":
		return session.PolecatSessionName(registry.PrefixForRig(rig), name)
	default:
		prefix := registry.PrefixForRig(rig)
		if prefix == "" {
			prefix = "gt"
		}
		return prefix + "-" + role
	}
}

// alternateSessionNames returns alternative session name derivations to try.
// Handles cases where the prefix registry hasn't resolved yet or uses hq-.
func alternateSessionNames(rig, role, name string) []string {
	var alts []string
	for _, p := range []string{"gt", "hq"} {
		alt := deriveSessionNameFromComponents(p, role, name)
		alts = append(alts, alt)
	}
	return alts
}

// deriveSessionNameFromComponents builds a session name from a specific prefix.
func deriveSessionNameFromComponents(prefix, role, name string) string {
	switch role {
	case "mayor":
		return prefix + "-mayor"
	case "deacon":
		if name == "boot" {
			return prefix + "-boot"
		}
		return prefix + "-deacon"
	case "witness":
		return prefix + "-witness"
	case "refinery":
		return prefix + "-refinery"
	case "crew":
		return prefix + "-crew-" + name
	case "polecat":
		return prefix + "-" + name
	case "dog":
		return prefix + "-dog-" + name
	default:
		return prefix + "-" + role
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

	// Store last few non-empty lines for tooltip
	lines := strings.Split(content, "\n")
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

// detectAgentType reads GT_AGENT from the tmux session environment.
// Returns "claude" if GT_AGENT is explicitly set to claude.
// Returns the value of GT_AGENT if set to something else.
// Returns "" (unknown) if GT_AGENT is not set — caller should use
// detectAgentTypeFromPane() on subsequent polls to identify from pane content.
func detectAgentType(sessionName string) string {
	cmd := exec.Command("tmux", "show-environment", "-t", sessionName, "GT_AGENT")
	out, err := cmd.Output()
	if err != nil {
		return "" // GT_AGENT not set — unknown, detect from pane content later
	}
	// Output format: GT_AGENT=opencode
	line := strings.TrimSpace(string(out))
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "" // empty value — unknown
	}
	return parts[1]
}

// detectAgentTypeFromPane identifies the agent type by inspecting pane content.
// OpenCode has distinctive signatures: "OpenCode" in the bottom status bar,
// box-drawing chrome (┃, ╹▀), and "esc interrupt" without Claude's ❯ prompt.
// Returns "opencode" or "claude" (fallback).
func detectAgentTypeFromPane(lines []string) string {
	for _, line := range lines {
		// OpenCode version string in bottom bar: "• OpenCode 1.1.60"
		if strings.Contains(line, "OpenCode") {
			return "opencode"
		}
		// OpenCode's bottom bar: "ctrl+t variants  tab agents  ctrl+p commands"
		if strings.Contains(line, "ctrl+p commands") && strings.Contains(line, "tab agents") {
			return "opencode"
		}
	}
	return "claude" // default fallback
}

// isClaudeAgent returns true if the agent type represents a Claude Code session.
// Empty string or "claude" both indicate Claude (the default).
func isClaudeAgent(agentType string) bool {
	return agentType == "" || agentType == "claude"
}

// View renders the TUI.
func (m *Model) View() string {
	return m.render()
}

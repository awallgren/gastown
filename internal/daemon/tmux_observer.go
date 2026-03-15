package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/tmux"
)

// TmuxObserver polls tmux sessions and emits agent_observation and
// state_snapshot events to .events.jsonl. This provides real-time agent
// activity data to consumers (GTP plugin, gt feed, gt top) without
// requiring them to do their own tmux polling.
//
// Phase 1 (lightweight): uses tmux window_activity timestamps + heartbeat
// state to compute activity levels. No pane content parsing.
type TmuxObserver struct {
	townRoot string
	tmux     *tmux.Tmux
	logger   func(format string, args ...interface{})
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	// Internal state: last-known agent states for change detection.
	agents map[string]*observedAgent

	// Snapshot counter: emit full snapshot every N ticks.
	tickCount    int
	snapshotFreq int // emit snapshot every this many ticks
}

// observedAgent tracks the last-emitted state for an agent,
// so we only emit events when something actually changes.
type observedAgent struct {
	name       string
	role       string
	rig        string
	icon       string
	level      string
	state      string // heartbeat state (working/idle/stuck/exiting)
	bead       string // hooked bead ID from heartbeat
	lastChange time.Time
	activity   int64 // last window_activity timestamp
}

const (
	observerPollInterval = 3 * time.Second
	observerSnapshotFreq = 10 // emit snapshot every 10 ticks (30s at 3s interval)
)

// NewTmuxObserver creates a new observer. Follows the KRCPruner pattern.
func NewTmuxObserver(townRoot string, t *tmux.Tmux, logger func(format string, args ...interface{})) *TmuxObserver {
	ctx, cancel := context.WithCancel(context.Background())
	return &TmuxObserver{
		townRoot:     townRoot,
		tmux:         t,
		logger:       logger,
		ctx:          ctx,
		cancel:       cancel,
		agents:       make(map[string]*observedAgent),
		snapshotFreq: observerSnapshotFreq,
	}
}

// Start begins the observer goroutine.
func (o *TmuxObserver) Start() error {
	o.wg.Add(1)
	go o.run()
	return nil
}

// Stop gracefully stops the observer.
func (o *TmuxObserver) Stop() {
	o.cancel()
	o.wg.Wait()
}

// run is the main observer loop.
func (o *TmuxObserver) run() {
	defer o.wg.Done()

	// Run an initial observation immediately on startup.
	o.observe()

	ticker := time.NewTicker(observerPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
			o.observe()
		}
	}
}

// observe runs a single observation cycle.
func (o *TmuxObserver) observe() {
	sessions, err := o.pollSessions()
	if err != nil {
		o.logger("observer: tmux poll error: %v", err)
		return
	}

	now := time.Now()
	seen := make(map[string]bool, len(sessions))

	for _, s := range sessions {
		seen[s.name] = true
		o.processSession(s, now)
	}

	// Mark dead agents that are no longer in tmux.
	for sessName, agent := range o.agents {
		if !seen[sessName] && agent.level != "dead" {
			agent.level = "dead"
			agent.state = ""
			o.emitObservation(agent, sessName)
		}
	}

	// Emit snapshot periodically.
	o.tickCount++
	if o.tickCount%o.snapshotFreq == 0 {
		o.emitSnapshot()
	}
}

// sessionPoll holds the raw data from a single tmux session poll.
type sessionPoll struct {
	name     string
	activity int64
	created  int64
}

// pollSessions queries tmux for Gas Town session activity.
// Uses the same format string as gt top: session_name|window_activity|session_created
func (o *TmuxObserver) pollSessions() ([]sessionPoll, error) {
	sessions, err := o.tmux.ListSessions()
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}

	// Get activity timestamps for all GT sessions in a single tmux call.
	// We use list-sessions with a richer format to avoid N subprocess calls.
	out, err := tmux.BuildCommand("list-sessions", "-F", "#{session_name}|#{window_activity}|#{session_created}").Output()
	if err != nil {
		// Fallback: return sessions without activity data
		return nil, fmt.Errorf("list-sessions with activity: %w", err)
	}

	var result []sessionPoll
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]

		// Filter to GT-managed sessions only.
		if !session.IsKnownSession(name) {
			continue
		}

		var ts int64
		if _, err := fmt.Sscanf(parts[1], "%d", &ts); err != nil || ts == 0 {
			continue
		}
		var created int64
		if len(parts) >= 3 {
			fmt.Sscanf(parts[2], "%d", &created) //nolint:errcheck
		}

		result = append(result, sessionPoll{name: name, activity: ts, created: created})
	}

	return result, nil
}

// processSession updates the internal state for a single session and emits
// an event if the state changed.
func (o *TmuxObserver) processSession(s sessionPoll, now time.Time) {
	agent, exists := o.agents[s.name]
	if !exists {
		agent = &observedAgent{}
		o.agents[s.name] = agent
		o.parseIdentity(agent, s.name)
	}

	// Track activity timestamp changes.
	if s.activity != agent.activity {
		agent.lastChange = now
		agent.activity = s.activity
	}

	// Compute activity level from time since last change.
	newLevel := computeLevel(now.Sub(agent.lastChange))

	// Read heartbeat for state info.
	hb := polecat.ReadSessionHeartbeat(o.townRoot, s.name)
	newState := ""
	newBead := ""
	if hb != nil {
		newState = string(hb.EffectiveState())
		newBead = hb.Bead
	}

	// Only emit if something changed.
	if newLevel != agent.level || newState != agent.state || newBead != agent.bead {
		agent.level = newLevel
		agent.state = newState
		agent.bead = newBead
		o.emitObservation(agent, s.name)
	}
}

// computeLevel maps duration since last activity change to a level string.
// Matches the thresholds from gt top (internal/tui/activity/model.go).
func computeLevel(since time.Duration) string {
	switch {
	case since < 3*time.Second:
		return "active"
	case since < 30*time.Second:
		return "recent"
	case since < 2*time.Minute:
		return "warm"
	case since < 5*time.Minute:
		return "cool"
	default:
		return "cold"
	}
}

// parseIdentity extracts agent name, role, rig, and icon from a session name.
// Mirrors the logic in internal/tui/activity/model.go parseSessionName().
func (o *TmuxObserver) parseIdentity(agent *observedAgent, sessName string) {
	// Check for dog sessions first (same as gt top).
	registry := session.DefaultRegistry()
	for _, prefix := range registry.Prefixes() {
		dogMarker := prefix + "-dog-"
		if strings.HasPrefix(sessName, dogMarker) {
			agent.rig = "hq"
			agent.role = constants.RoleDog
			agent.name = strings.TrimPrefix(sessName, dogMarker)
			agent.icon = constants.EmojiDog
			return
		}
	}
	for _, fallback := range []string{"hq-dog-", "gt-dog-"} {
		if strings.HasPrefix(sessName, fallback) {
			agent.rig = "hq"
			agent.role = constants.RoleDog
			agent.name = strings.TrimPrefix(sessName, fallback)
			agent.icon = constants.EmojiDog
			return
		}
	}

	id, err := session.ParseSessionName(sessName)
	if err != nil {
		agent.name = sessName
		agent.icon = "❓"
		return
	}

	switch id.Role {
	case session.RoleMayor:
		agent.rig = "hq"
		agent.role = constants.RoleMayor
		agent.name = "Mayor"
		agent.icon = constants.EmojiMayor
	case session.RoleDeacon:
		agent.rig = "hq"
		agent.role = constants.RoleDeacon
		if id.Name == "boot" {
			agent.name = "Boot"
		} else {
			agent.name = "Deacon"
		}
		agent.icon = constants.EmojiDeacon
	case session.RoleWitness:
		agent.rig = id.Rig
		agent.role = constants.RoleWitness
		agent.name = "witness"
		agent.icon = constants.EmojiWitness
	case session.RoleRefinery:
		agent.rig = id.Rig
		agent.role = constants.RoleRefinery
		agent.name = "refinery"
		agent.icon = constants.EmojiRefinery
	case session.RoleCrew:
		agent.rig = id.Rig
		agent.role = constants.RoleCrew
		agent.name = id.Name
		agent.icon = constants.EmojiCrew
	case session.RolePolecat:
		if id.Rig == "" && id.Name == "overseer" {
			agent.rig = "hq"
			agent.role = constants.RolePolecat
			agent.name = "overseer"
			agent.icon = "👤"
		} else {
			agent.rig = id.Rig
			agent.role = constants.RolePolecat
			agent.name = id.Name
			agent.icon = constants.EmojiPolecat
		}
	default:
		agent.name = sessName
		agent.icon = "❓"
	}
}

// emitObservation emits an agent_observation event for a single agent.
func (o *TmuxObserver) emitObservation(agent *observedAgent, sessName string) {
	payload := events.AgentObservationPayload(
		sessName,
		agent.name,
		agent.role,
		agent.rig,
		agent.level,
		agent.state,
	)
	if agent.bead != "" {
		payload["bead"] = agent.bead
	}
	if agent.icon != "" {
		payload["icon"] = agent.icon
	}
	if err := events.LogWithSource("daemon", events.TypeAgentObservation, "daemon/observer", payload, events.VisibilityAudit); err != nil {
		o.logger("observer: failed to emit observation: %v", err)
	}
}

// emitSnapshot emits a state_snapshot event with the full agent roster.
func (o *TmuxObserver) emitSnapshot() {
	var agentMaps []map[string]interface{}
	for sessName, agent := range o.agents {
		m := map[string]interface{}{
			"session": sessName,
			"name":    agent.name,
			"role":    agent.role,
			"rig":     agent.rig,
			"level":   agent.level,
			"icon":    agent.icon,
		}
		if agent.state != "" {
			m["state"] = agent.state
		}
		if agent.bead != "" {
			m["bead"] = agent.bead
		}
		agentMaps = append(agentMaps, m)
	}

	payload := events.StateSnapshotPayload(agentMaps)
	if err := events.LogWithSource("daemon", events.TypeStateSnapshot, "daemon/observer", payload, events.VisibilityAudit); err != nil {
		o.logger("observer: failed to emit snapshot: %v", err)
	}
}

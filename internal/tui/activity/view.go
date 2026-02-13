package activity

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/steveyegge/gastown/internal/ui"
)

// Block characters for LED visualization
const (
	blockFull   = "████"
	blockBright = "▓▓▓▓"
	blockMedium = "▒▒▒▒"
	blockDim    = "░░░░"
	blockDot    = " ·· "
)

// Sparkle characters that cycle through for active agents
var sparkleFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// Colors
var (
	colorActive      = lipgloss.AdaptiveColor{Light: "#86b300", Dark: "#c2d94c"} // bright green
	colorRecent      = lipgloss.AdaptiveColor{Light: "#399ee6", Dark: "#59c2ff"} // blue
	colorWarm        = lipgloss.AdaptiveColor{Light: "#f2ae49", Dark: "#ffb454"} // yellow
	colorCool        = lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#6c7680"} // gray
	colorCold        = lipgloss.AdaptiveColor{Light: "#5c6166", Dark: "#3e4449"} // dark gray
	colorRateLimited = lipgloss.AdaptiveColor{Light: "#ff8f40", Dark: "#ff8f40"} // orange
	colorWaiting     = lipgloss.AdaptiveColor{Light: "#f07171", Dark: "#f07178"} // RED - demands attention
	colorTitle       = lipgloss.AdaptiveColor{Light: "#399ee6", Dark: "#59c2ff"} // blue
	colorDim         = ui.ColorMuted
	colorBorder      = lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#4a5058"}
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorTitle)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorDim).
			Italic(true)

	rigHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorTitle).
			PaddingLeft(1)

	nameActiveStyle = lipgloss.NewStyle().
			Foreground(colorActive).
			Bold(true)

	nameRecentStyle = lipgloss.NewStyle().
			Foreground(colorRecent)

	nameWarmStyle = lipgloss.NewStyle().
			Foreground(colorWarm)

	nameCoolStyle = lipgloss.NewStyle().
			Foreground(colorCool)

	nameColdStyle = lipgloss.NewStyle().
			Foreground(colorCold)

	nameRateLimitedStyle = lipgloss.NewStyle().
				Foreground(colorRateLimited).
				Bold(true)

	nameWaitingStyle = lipgloss.NewStyle().
				Foreground(colorWaiting).
				Bold(true)

	barActiveStyle = lipgloss.NewStyle().
			Foreground(colorActive).
			Bold(true)

	barActiveDimStyle = lipgloss.NewStyle().
				Foreground(colorActive)

	barRecentStyle = lipgloss.NewStyle().
			Foreground(colorRecent)

	barWarmStyle = lipgloss.NewStyle().
			Foreground(colorWarm)

	barCoolStyle = lipgloss.NewStyle().
			Foreground(colorCool)

	barColdStyle = lipgloss.NewStyle().
			Foreground(colorCold)

	barRateLimitedStyle = lipgloss.NewStyle().
				Foreground(colorRateLimited).
				Bold(true)

	barWaitingStyle = lipgloss.NewStyle().
			Foreground(colorWaiting).
			Bold(true)

	barWaitingDimStyle = lipgloss.NewStyle().
				Foreground(colorWaiting)

	statActiveStyle = lipgloss.NewStyle().
			Foreground(colorActive).
			Bold(true)

	statRecentStyle = lipgloss.NewStyle().
			Foreground(colorRecent)

	statWarmStyle = lipgloss.NewStyle().
			Foreground(colorWarm)

	statColdStyle = lipgloss.NewStyle().
			Foreground(colorCold)

	statRateLimitedStyle = lipgloss.NewStyle().
				Foreground(colorRateLimited).
				Bold(true)

	statWaitingStyle = lipgloss.NewStyle().
				Foreground(colorWaiting).
				Bold(true)

	statusDimStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	statusWaitingStyle = lipgloss.NewStyle().
				Foreground(colorWaiting).
				Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	outerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(colorBorder).
				Padding(0, 1)

)

// render produces the full TUI output.
func (m *Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	// Reset render positions
	currentY := 2 // Start after header

	var sections []string

	// Header
	sections = append(sections, m.renderHeader())

	if m.totalAgents == 0 {
		sections = append(sections, "")
		sections = append(sections, subtitleStyle.Render("  No agent sessions running."))
		sections = append(sections, subtitleStyle.Render("  Start agents with: gt mayor start"))
	} else {
		// Rig panels
		for _, rig := range m.rigs {
			rigContent := m.renderRigWithPositions(rig, &currentY)
			sections = append(sections, rigContent)
		}
	}

	// Stats bar
	sections = append(sections, "")
	sections = append(sections, m.renderStats())

	// Help or hover detail (replaces help line when hovering)
	if m.hoveredAgent != nil {
		sections = append(sections, m.renderHoverDetail())
	} else {
		sections = append(sections, m.renderHelp())
	}

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Apply outer border
	maxW := m.width - 4
	if maxW < 30 {
		maxW = 30
	}
	return outerBorderStyle.Width(maxW).Render(content)
}

// renderHeader renders the title bar.
func (m *Model) renderHeader() string {
	// Animated sparkle
	sparkle := sparkleFrames[m.tickNum%len(sparkleFrames)]
	sparkleStyle := lipgloss.NewStyle().Foreground(colorActive)

	title := titleStyle.Render("GAS TOWN")
	sub := subtitleStyle.Render("agent monitor")

	agentCount := ""
	if m.totalAgents > 0 {
		agentCount = subtitleStyle.Render(fmt.Sprintf("%d agents", m.totalAgents))
	}

	left := sparkleStyle.Render(sparkle) + " " + title + "  " + sub
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(agentCount) - 8
	if gap < 2 {
		gap = 2
	}

	return left + strings.Repeat(" ", gap) + agentCount
}

// renderRig renders all agents in a rig as a panel.
func (m *Model) renderRig(rig string) string {
	agents := m.agentsForRig(rig)
	if len(agents) == 0 {
		return ""
	}

	var lines []string

	// Separate infrastructure agents from workers
	var infra, workers []*AgentLight
	for _, a := range agents {
		switch a.Role {
		case "mayor", "deacon", "dog", "witness", "refinery":
			infra = append(infra, a)
		default:
			workers = append(workers, a)
		}
	}

	// Render infrastructure agents in a compact row
	if len(infra) > 0 {
		lines = append(lines, m.renderAgentRow(infra))
	}

	// Render workers in rows of up to 4
	for i := 0; i < len(workers); i += 4 {
		end := i + 4
		if end > len(workers) {
			end = len(workers)
		}
		lines = append(lines, m.renderAgentRow(workers[i:end]))
	}

	content := strings.Join(lines, "\n")

	// Rig header
	header := rigHeaderStyle.Render(rig)

	// Determine border color based on most active agent
	bestLevel := LevelCold
	for _, a := range agents {
		if a.Level < bestLevel {
			bestLevel = a.Level
		}
	}

	borderColor := colorBorder
	switch bestLevel {
	case LevelActive:
		borderColor = colorActive
	case LevelRecent:
		borderColor = colorRecent
	case LevelRateLimited:
		borderColor = colorRateLimited
	case LevelWarm:
		borderColor = colorWarm
	}

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	maxW := m.width - 8
	if maxW < 25 {
		maxW = 25
	}

	return header + "\n" + style.Width(maxW).Render(content)
}

// renderAgentRow renders a row of agent lights.
func (m *Model) renderAgentRow(agents []*AgentLight) string {
	var cells []string
	for _, a := range agents {
		cells = append(cells, m.renderLight(a))
	}
	return strings.Join(cells, "  ")
}

// renderLight renders a single agent line: icon name bar status elapsed
func (m *Model) renderLight(a *AgentLight) string {
	elapsed := time.Since(a.LastChangeTime)

	// Name styling based on activity level
	var nameStyle lipgloss.Style
	switch a.Level {
	case LevelActive:
		nameStyle = nameActiveStyle
	case LevelRecent:
		nameStyle = nameRecentStyle
	case LevelWarm:
		nameStyle = nameWarmStyle
	case LevelCool:
		nameStyle = nameCoolStyle
	case LevelCold:
		nameStyle = nameColdStyle
	case LevelRateLimited:
		nameStyle = nameRateLimitedStyle
	case LevelHitLimit:
		nameStyle = nameRateLimitedStyle // orange family, same as rate-limited
	case LevelWaitingForHuman:
		nameStyle = nameWaitingStyle
	}

	// Truncate long names
	displayName := a.Name
	if len(displayName) > 10 {
		displayName = displayName[:9] + "~"
	}
	// Pad name to fixed width for alignment
	displayName = fmt.Sprintf("%-10s", displayName)

	// Bar visualization - the actual "blinkenlights"
	bar := m.renderBar(a)

	// Status text + elapsed time
	var statusStr string
	var stStyle lipgloss.Style

	// Current tool execution takes priority (most specific/useful info)
	if a.CurrentTool != "" {
		statusStr = "⏺ " + a.CurrentTool
		stStyle = statusDimStyle
	} else {
		// Fall back to level-based status
		switch a.Level {
		case LevelActive:
			statusStr = a.StatusText
			stStyle = statusDimStyle
		case LevelRecent:
			statusStr = a.StatusText
			stStyle = statusDimStyle
		case LevelWarm, LevelCool:
			if a.StatusText != "" {
				statusStr = a.StatusText
			} else {
				statusStr = "idle"
			}
			stStyle = statusDimStyle
		case LevelCold:
			statusStr = "stalled"
			stStyle = lipgloss.NewStyle().Foreground(colorCold)
		case LevelRateLimited:
			statusStr = "rate limited"
			stStyle = lipgloss.NewStyle().Foreground(colorRateLimited)
		case LevelHitLimit:
			statusStr = "⚠ HIT LIMIT"
			if a.LimitResetInfo != "" {
				statusStr += " · " + a.LimitResetInfo
			}
			stStyle = statRateLimitedStyle
		case LevelWaitingForHuman:
			statusStr = "⚠ NEEDS HUMAN"
			if a.WaitingReason != "" {
				statusStr += " · " + a.WaitingReason
			}
			stStyle = statusWaitingStyle
		}
	}

	// Append session limit warning if known (more urgent than context)
	if a.SessionLimitPct > 0 {
		limitStr := renderSessionLimitIndicator(a.SessionLimitPct, a.SessionLimitReset)
		statusStr += " " + limitStr
	}

	// Append context indicator if known and low
	if a.ContextPercent > 0 {
		ctxBar := renderContextIndicator(a.ContextPercent)
		statusStr += " " + ctxBar
	}

	// Elapsed time
	elapsedStr := formatElapsed(elapsed)
	showElapsed := !strings.Contains(statusStr, "·") // skip when status has timing

	// Calculate available width for status text based on terminal size.
	// Layout: [border 8] icon(3) name(11) bar(4) gap(2) status(...) gap(2) elapsed(~8)
	fixedWidth := 20 // icon + name + bar + gap before status
	elapsedWidth := 0
	if showElapsed {
		elapsedWidth = len(elapsedStr) + 2 // gap + elapsed text
	}
	availableForStatus := m.width - 8 - fixedWidth - elapsedWidth
	if availableForStatus < 10 {
		availableForStatus = 10
	}

	// Truncate status to fit available space
	statusRunes := []rune(statusStr)
	if len(statusRunes) > availableForStatus {
		statusStr = string(statusRunes[:availableForStatus-3]) + "..."
	}

	// Build the line
	line := a.Icon + " " + nameStyle.Render(displayName) + " " + bar
	if statusStr != "" {
		line += "  " + stStyle.Render(statusStr)
	}
	if showElapsed {
		line += "  " + statusDimStyle.Render(elapsedStr)
	}

	return line
}

// renderBar renders the LED bar for an agent.
func (m *Model) renderBar(a *AgentLight) string {
	switch a.Level {
	case LevelActive:
		// Blinking effect: alternate between full and bright
		if m.blinkOn {
			return barActiveStyle.Render(blockFull)
		}
		return barActiveDimStyle.Render(blockBright)

	case LevelRecent:
		// Gentle pulse: alternate between full and bright
		if m.tickNum%4 < 2 {
			return barRecentStyle.Render(blockFull)
		}
		return barRecentStyle.Render(blockBright)

	case LevelWarm:
		return barWarmStyle.Render(blockMedium)

	case LevelCool:
		return barCoolStyle.Render(blockDim)

	case LevelCold:
		return barColdStyle.Render(blockDot)

	case LevelRateLimited:
		// Distinctive blinking pattern: medium blocks alternating
		if m.blinkOn {
			return barRateLimitedStyle.Render(blockMedium)
		}
		return barRateLimitedStyle.Render(blockDim)

	case LevelHitLimit:
		// Orange alarm blink - agent is dead until limit resets
		if m.blinkOn {
			return barRateLimitedStyle.Render("‼‼‼‼")
		}
		return barColdStyle.Render(blockDot)

	case LevelWaitingForHuman:
		// RED alarm blink - this agent needs you
		if m.blinkOn {
			return barWaitingStyle.Render("‼‼‼‼")
		}
		return barWaitingDimStyle.Render(blockMedium)

	default:
		return barColdStyle.Render(blockDot)
	}
}

// renderStats renders the stats bar.
func (m *Model) renderStats() string {
	if m.totalAgents == 0 {
		return ""
	}

	var parts []string

	// Waiting count comes FIRST - it's the most important signal
	if m.waitingCount > 0 {
		label := fmt.Sprintf("⚠ %d NEED HUMAN", m.waitingCount)
		parts = append(parts, statWaitingStyle.Render(label))
	}
	// Hit-limit count - second most important (agents are dead)
	if m.hitLimitCount > 0 {
		label := fmt.Sprintf("⚠ %d HIT LIMIT", m.hitLimitCount)
		parts = append(parts, statRateLimitedStyle.Render(label))
	}
	if m.activeCount > 0 {
		parts = append(parts, statActiveStyle.Render(fmt.Sprintf("%d active", m.activeCount)))
	}
	if m.recentCount > 0 {
		parts = append(parts, statRecentStyle.Render(fmt.Sprintf("%d recent", m.recentCount)))
	}
	if m.rateLimitedCount > 0 {
		parts = append(parts, statRateLimitedStyle.Render(fmt.Sprintf("%d rate-limited", m.rateLimitedCount)))
	}
	if m.idleCount > 0 {
		parts = append(parts, statWarmStyle.Render(fmt.Sprintf("%d idle", m.idleCount)))
	}
	if m.stuckCount > 0 {
		parts = append(parts, statColdStyle.Render(fmt.Sprintf("%d stuck", m.stuckCount)))
	}

	return "  " + strings.Join(parts, "  •  ")
}

// renderRigWithPositions renders a rig and tracks agent Y positions for hover detection.
// Each agent gets its own line to show status text and elapsed time.
func (m *Model) renderRigWithPositions(rig string, currentY *int) string {
	agents := m.agentsForRig(rig)
	if len(agents) == 0 {
		return ""
	}

	// Header takes 1 line
	*currentY++

	var lines []string
	*currentY++ // Border top line

	for _, a := range agents {
		a.renderY = *currentY
		a.renderHeight = 1
		lines = append(lines, m.renderLight(a))
		*currentY++
	}

	content := strings.Join(lines, "\n")

	// Rig header
	header := rigHeaderStyle.Render(rig)

	// Determine border color based on most active agent
	bestLevel := LevelCold
	for _, a := range agents {
		if a.Level < bestLevel {
			bestLevel = a.Level
		}
	}

	// Check if any agent in this rig needs human attention or hit limit
	hasWaiting := false
	hasHitLimit := false
	for _, a := range agents {
		if a.Level == LevelWaitingForHuman {
			hasWaiting = true
		}
		if a.Level == LevelHitLimit {
			hasHitLimit = true
		}
	}

	borderColor := colorBorder
	if hasWaiting {
		// RED border when any agent needs human - overrides everything
		borderColor = colorWaiting
	} else if hasHitLimit {
		// Orange border when any agent hit limit
		borderColor = colorRateLimited
	} else {
		switch bestLevel {
		case LevelActive:
			borderColor = colorActive
		case LevelRecent:
			borderColor = colorRecent
		case LevelRateLimited:
			borderColor = colorRateLimited
		case LevelWarm:
			borderColor = colorWarm
		}
	}

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)

	maxW := m.width - 8
	if maxW < 25 {
		maxW = 25
	}

	*currentY += 2 // Border lines
	return header + "\n" + style.Width(maxW).Render(content)
}

// renderHoverDetail renders a detail line for the hovered agent, shown in
// place of the help text. No floating overlay - just a clean inline detail.
func (m *Model) renderHoverDetail() string {
	a := m.hoveredAgent
	if a == nil {
		return m.renderHelp()
	}

	var parts []string
	parts = append(parts, lipgloss.NewStyle().Bold(true).Render(a.Icon+" "+a.SessionName))

	// Show current tool execution (highest priority info)
	if a.CurrentTool != "" {
		parts = append(parts, "⏺ "+a.CurrentTool)
	}

	// Show session limit warning if known
	if a.SessionLimitPct > 0 {
		limitInfo := fmt.Sprintf("session: %d%% used", a.SessionLimitPct)
		if a.SessionLimitReset != "" {
			limitInfo += " · " + a.SessionLimitReset
		}
		var limitStyle lipgloss.Style
		if a.SessionLimitPct >= 90 {
			limitStyle = lipgloss.NewStyle().Foreground(colorWaiting)
		} else {
			limitStyle = lipgloss.NewStyle().Foreground(colorRateLimited)
		}
		parts = append(parts, limitStyle.Render(limitInfo))
	}

	// Show context remaining if known
	if a.ContextPercent > 0 {
		ctxInfo := fmt.Sprintf("context: %d%%", a.ContextPercent)
		var ctxStyle lipgloss.Style
		if a.ContextPercent < 20 {
			ctxStyle = lipgloss.NewStyle().Foreground(colorWaiting)
		} else if a.ContextPercent < 40 {
			ctxStyle = lipgloss.NewStyle().Foreground(colorRateLimited)
		} else {
			ctxStyle = statusDimStyle
		}
		parts = append(parts, ctxStyle.Render(ctxInfo))
	}

	if a.CurrentBead != "" {
		parts = append(parts, "bead: "+a.CurrentBead)
	}

	// Show critical states
	if a.WaitingForHuman && a.WaitingReason != "" {
		parts = append(parts, statusWaitingStyle.Render("⚠ "+a.WaitingReason))
	} else if a.HitLimit {
		info := "⚠ HIT LIMIT"
		if a.LimitResetInfo != "" {
			info += " · " + a.LimitResetInfo
		}
		parts = append(parts, statRateLimitedStyle.Render(info))
	}

	elapsed := time.Since(a.LastChangeTime)
	parts = append(parts, "last activity: "+formatElapsed(elapsed)+" ago")

	return "  " + lipgloss.NewStyle().Foreground(colorTitle).Render(strings.Join(parts, "  ·  "))
}

// renderContextIndicator returns a compact text indicator for context remaining.
func renderContextIndicator(percent int) string {
	if percent <= 0 || percent > 100 {
		return ""
	}

	text := fmt.Sprintf("%d%% til compact", percent)

	var style lipgloss.Style
	switch {
	case percent < 20:
		style = lipgloss.NewStyle().Foreground(colorWaiting) // red
	case percent < 40:
		style = lipgloss.NewStyle().Foreground(colorRateLimited) // orange
	case percent < 60:
		style = lipgloss.NewStyle().Foreground(colorWarm) // yellow
	default:
		style = statusDimStyle
	}

	return style.Render(text)
}

// renderSessionLimitIndicator returns a compact text indicator for session usage limit.
func renderSessionLimitIndicator(pct int, resetInfo string) string {
	if pct <= 0 {
		return ""
	}

	text := fmt.Sprintf("%d%% session used", pct)
	if resetInfo != "" {
		text += " · " + resetInfo
	}

	var style lipgloss.Style
	switch {
	case pct >= 95:
		style = lipgloss.NewStyle().Foreground(colorWaiting) // red - about to die
	case pct >= 80:
		style = lipgloss.NewStyle().Foreground(colorRateLimited) // orange
	default:
		style = lipgloss.NewStyle().Foreground(colorWarm) // yellow
	}

	return style.Render(text)
}

// formatElapsed formats a duration compactly for inline display.
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		if s > 0 {
			return fmt.Sprintf("%dm %ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// renderHelp renders the help bar.
func (m *Model) renderHelp() string {
	return helpStyle.Render("  q: quit  •  hover for details  •  ⚠ = needs human input")
}

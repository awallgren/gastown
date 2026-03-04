package activity

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/steveyegge/gastown/internal/ui"
)

// Dot indicators for agent activity (replacing LED block bars)
const (
	dotActive = "●" // filled circle — active
	dotIdle   = "○" // open circle — idle/warm
	dotCold   = "·" // small dot — cold/stalled
)

// Sparkle characters that cycle through for active agents
var sparkleFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}

// Colors — subtler palette, keeps semantic meaning
var (
	colorActive      = lipgloss.AdaptiveColor{Light: "#5a8c00", Dark: "#a6c060"} // muted green
	colorRecent      = lipgloss.AdaptiveColor{Light: "#2e7eb3", Dark: "#6dafda"} // soft blue
	colorWarm        = lipgloss.AdaptiveColor{Light: "#b38600", Dark: "#d4a543"} // muted amber
	colorCool        = lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#606870"} // gray
	colorCold        = lipgloss.AdaptiveColor{Light: "#5c6166", Dark: "#3e4449"} // dark gray
	colorRateLimited = lipgloss.AdaptiveColor{Light: "#d97020", Dark: "#e08840"} // muted orange
	colorWaiting     = lipgloss.AdaptiveColor{Light: "#d04040", Dark: "#e05555"} // red (demands attention)
	colorCompacting  = lipgloss.AdaptiveColor{Light: "#8b3fbf", Dark: "#b070e0"} // purple — transient maintenance
	colorTitle       = lipgloss.AdaptiveColor{Light: "#2e7eb3", Dark: "#6dafda"} // soft blue
	colorDim         = ui.ColorMuted
	colorBorder      = lipgloss.AdaptiveColor{Light: "#828c99", Dark: "#404850"}
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

	nameCompactingStyle = lipgloss.NewStyle().
				Foreground(colorCompacting).
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

	barCompactingStyle = lipgloss.NewStyle().
				Foreground(colorCompacting).
				Bold(true)

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

	statusCompactingStyle = lipgloss.NewStyle().
				Foreground(colorCompacting)

	helpStyle = lipgloss.NewStyle().
			Foreground(colorDim)

	outerStyle = lipgloss.NewStyle().
			Padding(0, 1)
)

// render produces the full TUI output.
func (m *Model) render() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	// Reset render positions.
	// Without an outer border, the header is at screen Y=0.
	// Rig content follows immediately after the header.
	currentY := 0 // header line

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
	} else if flash := m.activeFlash(); flash != "" {
		sections = append(sections, m.renderFlash(flash))
	} else {
		sections = append(sections, m.renderHelp())
	}

	content := lipgloss.JoinVertical(lipgloss.Left, sections...)

	// Apply outer padding (no border — cleaner look)
	maxW := m.width - 2
	if maxW < 30 {
		maxW = 30
	}
	return outerStyle.Width(maxW).Render(content)
}

// renderHeader renders the title bar.
func (m *Model) renderHeader() string {
	// Animated sparkle
	sparkle := sparkleFrames[m.tickNum%len(sparkleFrames)]
	sparkleStyle := lipgloss.NewStyle().Foreground(colorActive)

	title := titleStyle.Render(m.townTitle())
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

	// Compacting overrides level-based name style — it's a transient
	// maintenance state that can happen at any activity level.
	if a.IsCompacting {
		nameStyle = nameCompactingStyle
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

	// Build work bead context — this is the primary info for the agent line.
	// Shows bead ID, title, and molecule step progress when available.
	var beadCtx string
	if a.WorkBeadID != "" {
		beadCtx = a.WorkBeadID
		if a.WorkBeadTitle != "" {
			beadCtx += ": " + a.WorkBeadTitle
		}
		if a.StepsTotal > 0 {
			beadCtx += fmt.Sprintf(" [%d/%d]", a.StepsDone, a.StepsTotal)
			if a.StepCurrent != "" {
				beadCtx += " " + a.StepCurrent
			}
		}
	}

	// Priority order:
	//   1. Warning states (HIT LIMIT, NEEDS HUMAN) — always override
	//   2. Compacting — transient maintenance, overrides normal work display
	//   3. Bead context as primary content (tool appended if active)
	//   4. Active tool alone (no bead info available)
	//   5. Fallback to StatusText or level-based defaults
	switch {
	case a.Level == LevelHitLimit:
		statusStr = "⚠ HIT LIMIT"
		if a.LimitResetInfo != "" {
			statusStr += " · " + a.LimitResetInfo
		}
		stStyle = statRateLimitedStyle
	case a.Level == LevelWaitingForHuman:
		statusStr = "⚠ NEEDS HUMAN"
		if a.WaitingReason != "" {
			statusStr += " · " + a.WaitingReason
		}
		stStyle = statusWaitingStyle
	case a.IsCompacting:
		statusStr = "COMPACTING"
		stStyle = statusCompactingStyle
	case beadCtx != "":
		switch a.Level {
		case LevelCold:
			statusStr = "stalled · " + beadCtx
			stStyle = lipgloss.NewStyle().Foreground(colorCold)
		case LevelRateLimited:
			statusStr = "rate limited · " + beadCtx
			stStyle = lipgloss.NewStyle().Foreground(colorRateLimited)
		default:
			statusStr = beadCtx
			if a.CurrentTool != "" {
				statusStr += " · ⏺ " + a.CurrentTool
			}
			stStyle = statusDimStyle
		}
	case a.CurrentTool != "":
		statusStr = "⏺ " + a.CurrentTool
		stStyle = statusDimStyle
	default:
		// No bead info — fall back to patrol status, then level/status text
		switch a.Level {
		case LevelActive, LevelRecent:
			if a.StatusText != "" {
				statusStr = a.StatusText
			} else if a.LastPatrol != "" {
				statusStr = a.LastPatrol
			}
			stStyle = statusDimStyle
		case LevelWarm, LevelCool:
			if a.StatusText != "" {
				statusStr = a.StatusText
			} else if a.LastPatrol != "" {
				statusStr = a.LastPatrol
			} else {
				statusStr = "idle"
			}
			stStyle = statusDimStyle
		case LevelCold:
			if a.LastPatrol != "" {
				statusStr = "stalled · " + a.LastPatrol
			} else {
				statusStr = "stalled"
			}
			stStyle = lipgloss.NewStyle().Foreground(colorCold)
		case LevelRateLimited:
			if a.LastPatrol != "" {
				statusStr = "rate limited · " + a.LastPatrol
			} else {
				statusStr = "rate limited"
			}
			stStyle = lipgloss.NewStyle().Foreground(colorRateLimited)
		}
	}

	// Elapsed time — shown right-justified alongside context/compaction info
	elapsedStr := formatElapsed(elapsed)
	showElapsed := elapsedStr != ""

	// Right-justified indicators: elapsed, session limit %, context %.
	// Build both full and compact versions to measure exact widths,
	// then use the measurement to constrain the status text.

	// Build right-side string (full version first)
	buildRightSide := func(compact bool) string {
		var rs string
		if showElapsed {
			rs += statusDimStyle.Render(elapsedStr)
		}
		if a.SessionLimitPct > 0 {
			if rs != "" {
				rs += "  "
			}
			rs += renderSessionLimitIndicator(a.SessionLimitPct, a.SessionLimitReset)
		}
		if a.ContextPercent > 0 {
			if rs != "" {
				rs += " "
			}
			rs += renderContextIndicator(a.ContextPercent, compact, a.TokenCount, a.SessionCreated)
		}
		return rs
	}

	rightFull := buildRightSide(false)
	rightCompact := buildRightSide(true)
	rightFullWidth := lipgloss.Width(rightFull)
	rightCompactWidth := lipgloss.Width(rightCompact)
	if rightFullWidth > 0 {
		rightFullWidth += 3 // minimum 3-space gap from left content
		rightCompactWidth += 3
	}

	// Measure actual visual width of the fixed prefix (handles emoji + ANSI correctly)
	prefix := a.Icon + " " + nameStyle.Render(displayName) + " " + bar + "  "
	prefixWidth := lipgloss.Width(prefix)

	// Content width inside rig panel: rig border(4) + outer padding(2) + safety(2)
	contentWidth := m.width - 8
	leftBudget := contentWidth - prefixWidth - rightFullWidth
	if leftBudget < 10 {
		leftBudget = 10
	}

	// See if we need compact mode for right indicators
	useCompact := false
	availableForStatus := leftBudget
	if availableForStatus < 10 {
		// Try with compact indicators
		leftBudget = contentWidth - prefixWidth - rightCompactWidth
		if leftBudget < 10 {
			leftBudget = 10
		}
		availableForStatus = leftBudget
		if availableForStatus < 10 {
			availableForStatus = 10
		}
		useCompact = true
	}

	// Truncate status to fit available visual width.
	// Uses lipgloss.Width (visual columns) not rune count, because some
	// characters (em-dash, CJK, emoji) take 2 columns per rune.
	if lipgloss.Width(statusStr) > availableForStatus {
		statusRunes := []rune(statusStr)
		cut := len(statusRunes)
		for cut > 0 && lipgloss.Width(string(statusRunes[:cut])+"...") > availableForStatus {
			cut--
		}
		if cut < len(statusRunes) {
			// Strip trailing punctuation so "passed...." doesn't happen
			truncated := statusRunes[:cut]
			for len(truncated) > 0 {
				last := truncated[len(truncated)-1]
				if last == '.' || last == ',' || last == ' ' || last == '·' {
					truncated = truncated[:len(truncated)-1]
				} else {
					break
				}
			}
			statusStr = string(truncated) + "..."
		}
	}

	// Build the left side of the line
	line := a.Icon + " " + nameStyle.Render(displayName) + " " + bar
	if statusStr != "" {
		line += "  " + stStyle.Render(statusStr)
	}
	leftWidth := lipgloss.Width(line)

	// Append right-justified indicators with gap fill
	rightSide := rightFull
	if useCompact {
		rightSide = rightCompact
	}

	if rightSide != "" {
		rightWidth := lipgloss.Width(rightSide)
		gap := contentWidth - leftWidth - rightWidth
		if gap < 3 {
			gap = 3
		}
		line += strings.Repeat(" ", gap) + rightSide
	}

	return line
}

// renderBar renders the activity dot indicator for an agent.
func (m *Model) renderBar(a *AgentLight) string {
	// Compacting overrides level-based bar — steady purple dot
	if a.IsCompacting {
		return barCompactingStyle.Render(dotActive)
	}

	switch a.Level {
	case LevelActive:
		// Blink between bright and dim for active agents
		if m.blinkOn {
			return barActiveStyle.Render(dotActive)
		}
		return barActiveDimStyle.Render(dotActive)

	case LevelRecent:
		return barRecentStyle.Render(dotActive)

	case LevelWarm:
		return barWarmStyle.Render(dotIdle)

	case LevelCool:
		return barCoolStyle.Render(dotIdle)

	case LevelCold:
		return barColdStyle.Render(dotCold)

	case LevelRateLimited:
		// Blink for rate-limited
		if m.blinkOn {
			return barRateLimitedStyle.Render(dotActive)
		}
		return barRateLimitedStyle.Render(dotIdle)

	case LevelHitLimit:
		// Alarm blink — agent is dead until limit resets
		if m.blinkOn {
			return barRateLimitedStyle.Render("‼")
		}
		return barColdStyle.Render(dotCold)

	case LevelWaitingForHuman:
		// RED alarm blink — this agent needs you
		if m.blinkOn {
			return barWaitingStyle.Render("‼")
		}
		return barWaitingDimStyle.Render(dotIdle)

	default:
		return barColdStyle.Render(dotCold)
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
	*currentY++ // Border top line (╭──...──╮); first agent is next row

	for _, a := range agents {
		*currentY++
		a.renderY = *currentY
		a.renderHeight = 1
		lines = append(lines, m.renderLight(a))
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

	maxW := m.width - 6
	if maxW < 25 {
		maxW = 25
	}

	*currentY += 1 // Border bottom line
	return header + "\n" + style.Width(maxW).Render(content)
}

// renderHoverDetail renders a detail line for the hovered agent, shown in
// place of the help text. Only shows info NOT already visible on the agent line
// (which already shows: status/tool, elapsed, context %, session limit %, critical states).
func (m *Model) renderHoverDetail() string {
	a := m.hoveredAgent
	if a == nil {
		return m.renderHelp()
	}

	var parts []string
	parts = append(parts, lipgloss.NewStyle().Bold(true).Render(a.Icon+" "+a.SessionName))

	// Show agent type for non-Claude agents (Claude is the default, so showing it is noise)
	if a.AgentType != "" && a.AgentType != "claude" {
		parts = append(parts, statusDimStyle.Render("agent: "+a.AgentType))
	}

	// Work assignment from beads DB
	if a.WorkBeadID != "" {
		workInfo := a.WorkBeadID
		if a.WorkBeadTitle != "" {
			workInfo += ": " + a.WorkBeadTitle
		}
		if a.StepsTotal > 0 {
			stepInfo := fmt.Sprintf(" [%d/%d]", a.StepsDone, a.StepsTotal)
			if a.StepCurrent != "" {
				stepInfo += " " + a.StepCurrent
			}
			workInfo += stepInfo
		}
		parts = append(parts, workInfo)
	}

	// Session limit reset time — the % is on the agent line, but reset info is only here
	if a.SessionLimitPct > 0 && a.SessionLimitReset != "" {
		limitInfo := fmt.Sprintf("limit resets %s", a.SessionLimitReset)
		var limitStyle lipgloss.Style
		if a.SessionLimitPct >= 90 {
			limitStyle = lipgloss.NewStyle().Foreground(colorWaiting)
		} else {
			limitStyle = lipgloss.NewStyle().Foreground(colorRateLimited)
		}
		parts = append(parts, limitStyle.Render(limitInfo))
	}

	// Session uptime — helps spot spontaneous restarts
	if !a.SessionCreated.IsZero() {
		uptime := time.Since(a.SessionCreated)
		parts = append(parts, "up "+formatElapsed(uptime))
	}

	return "  " + lipgloss.NewStyle().Foreground(colorTitle).Render(strings.Join(parts, "  ·  "))
}

// renderContextIndicator returns a compact text indicator for context usage
// and optionally the token rate (k/hr).
// percent is context *remaining* (0-100); we display 100-percent as "used".
// If compact is true, renders just "xx%" instead of "xx% used".
// tokenCount and sessionCreated are used to compute and display token rate.
func renderContextIndicator(percent int, compact bool, tokenCount int, sessionCreated time.Time) string {
	if percent <= 0 || percent > 100 {
		return ""
	}

	used := 100 - percent

	// Compute token rate (k/hr) if we have data
	var rateStr string
	if tokenCount > 0 && !sessionCreated.IsZero() {
		hours := time.Since(sessionCreated).Hours()
		if hours > 0.01 { // at least ~36 seconds of uptime
			rateKhr := float64(tokenCount) / hours / 1000.0
			if rateKhr >= 100 {
				rateStr = fmt.Sprintf("%.0fk/hr", rateKhr)
			} else if rateKhr >= 10 {
				rateStr = fmt.Sprintf("%.0fk/hr", rateKhr)
			} else {
				rateStr = fmt.Sprintf("%.1fk/hr", rateKhr)
			}
		}
	}

	// rateStr is computed above but hidden from display for now.
	_ = rateStr

	var text string
	if compact {
		text = fmt.Sprintf("%d%%", used)
	} else {
		text = fmt.Sprintf("%d%% used", used)
	}

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
	if d < 10*time.Second {
		return ""
	}
	if d < time.Minute {
		secs := int(d.Seconds())
		return fmt.Sprintf("%ds", secs)
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
	return helpStyle.Render("  q: quit  •  double-click: attach  •  ⚠ = needs human")
}

// activeFlash returns the current flash message if it's still within its display window (3s).
func (m *Model) activeFlash() string {
	if m.flashMessage == "" {
		return ""
	}
	if time.Since(m.flashTime) > 3*time.Second {
		m.flashMessage = ""
		return ""
	}
	return m.flashMessage
}

// renderFlash renders a flash status message in the help bar area.
func (m *Model) renderFlash(msg string) string {
	style := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
	return "  " + style.Render(msg)
}

// townTitle returns the display title for the header.
// Uses the town name from town.json if available, otherwise "GAS TOWN".
func (m *Model) townTitle() string {
	if m.townName != "" {
		return strings.ToUpper(m.townName)
	}
	return "GAS TOWN"
}

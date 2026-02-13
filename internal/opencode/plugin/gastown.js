// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events,
// and emits tool execution events for gt top agent monitoring.
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const autonomousRoles = new Set(["polecat", "witness", "refinery", "deacon"]);
  let didInit = false;
  let tmuxSession = null;

  const run = async (cmd) => {
    try {
      await $`/bin/sh -lc ${cmd}`.cwd(directory);
    } catch (err) {
      console.error(`[gastown] ${cmd} failed`, err?.message || err);
    }
  };

  // Fire-and-forget: emit event without blocking the agent.
  const emit = (cmd) => {
    $`/bin/sh -lc ${cmd}`.cwd(directory).catch(() => {});
  };

  // Get tmux session name (cached) for event matching in gt top.
  const getSession = async () => {
    if (tmuxSession) return tmuxSession;
    try {
      const result = await $`tmux display-message -p #S`.cwd(directory);
      tmuxSession = (result.stdout || "").trim();
    } catch {
      tmuxSession = "";
    }
    return tmuxSession;
  };

  // Shell-escape a string for safe embedding in gt top emit arguments.
  const esc = (s) => (s || "").replace(/['"\\$`!]/g, "").slice(0, 80);

  const injectContext = async () => {
    await run("gt prime");
    if (autonomousRoles.has(role)) {
      await run("gt mail check --inject");
    }
    // NOTE: session-started nudge to deacon removed — it interrupted
    // the deacon's await-signal backoff. Deacon wakes on beads activity.
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (didInit) return;
        didInit = true;
        await injectContext();
      }
      if (event?.type === "session.compacted") {
        await injectContext();
      }
      if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) {
          await $`gt costs record --session ${sessionID}`.catch(() => {});
        }
      }
    },

    // Tool execution tracking for gt top agent monitor.
    // These events populate the CurrentTool field in gt top's LED display,
    // giving visibility into what OpenCode agents are doing without
    // parsing the box-drawing-character TUI via tmux capture-pane.
    "tool.execute.before": async ({ tool }) => {
      const session = await getSession();
      const toolName = esc(tool?.name || "unknown");
      const toolInput = esc(
        typeof tool?.input === "string"
          ? tool.input
          : JSON.stringify(tool?.input || ""),
      );
      const toolInfo = toolInput ? `${toolName}(${toolInput})` : toolName;
      emit(
        `gt top emit tool_started --actor ${esc(role)} --status "${toolInfo}" --message "${session}"`,
      );
    },

    "tool.execute.after": async ({ tool }) => {
      const session = await getSession();
      const toolName = esc(tool?.name || "unknown");
      emit(
        `gt top emit tool_finished --actor ${esc(role)} --status "${toolName}" --message "${session}"`,
      );
    },

    "experimental.session.compacting": async ({ sessionID }, output) => {
      const roleDisplay = role || "unknown";
      output.context.push(`
## Gas Town Multi-Agent System

**After Compaction:** Run \`gt prime\` to restore full context.
**Check Hook:** \`gt hook\` - if work present, execute immediately (GUPP).
**Role:** ${roleDisplay}
`);
    },
  };
};

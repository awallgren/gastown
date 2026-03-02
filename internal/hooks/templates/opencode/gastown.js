// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events,
// and emits tool execution events for gt top agent monitoring.
// Injects gt prime context into the system prompt via experimental.chat.system.transform.
export const GasTown = async ({ $, directory }) => {
  const role = (process.env.GT_ROLE || "").toLowerCase();
  const autonomousRoles = new Set(["polecat", "witness", "refinery", "deacon"]);
  let didInit = false;
  let tmuxSession = null;

  // Promise-based context loading ensures the system transform hook can
  // await the result even if session.created hasn't resolved yet.
  let primePromise = null;

  const captureRun = async (cmd) => {
    try {
      // .text() captures stdout as a string and suppresses terminal echo.
      return await $`/bin/sh -lc ${cmd}`.cwd(directory).text();
    } catch (err) {
      console.error(`[gastown] ${cmd} failed`, err?.message || err);
      return "";
    }
  };

  // Fire-and-forget: emit event without blocking the agent.
  const emit = (cmd) => {
    $`/bin/sh -lc ${cmd}`.cwd(directory).catch(() => {});
  };

  // Get tmux session name (cached) for event matching in gt top.
  // NOTE: The format token must be interpolated — Bun's shell treats
  // bare `#` as a comment character, so `#S` would be silently eaten.
  // Interpolated values are passed as literal string arguments.
  // .quiet() prevents stdout from leaking into the OpenCode TUI.
  const getSession = async () => {
    if (tmuxSession) return tmuxSession;
    try {
      const fmt = "#S";
      const result = await $`tmux display-message -p ${fmt}`.quiet().cwd(directory);
      tmuxSession = (result.stdout || "").toString().trim();
    } catch {
      tmuxSession = "";
    }
    return tmuxSession;
  };

  // Shell-escape a string for safe embedding in gt top emit arguments.
  const esc = (s) => (s || "").replace(/['"\\$`!]/g, "").slice(0, 80);

  const loadPrime = async () => {
    let context = await captureRun("gt prime");
    if (autonomousRoles.has(role)) {
      const mail = await captureRun("gt mail check --inject");
      if (mail) {
        context += "\n" + mail;
      }
    }
    // NOTE: session-started nudge to deacon removed — it interrupted
    // the deacon's await-signal backoff. Deacon wakes on beads activity.
    return context;
  };

  return {
    event: async ({ event }) => {
      if (event?.type === "session.created") {
        if (didInit) return;
        didInit = true;
        // Start loading prime context early; system.transform will await it.
        primePromise = loadPrime();
      }
      if (event?.type === "session.compacted") {
        // Signal compaction finished to gt top, then reload prime context.
        const session = await getSession();
        emit(
          `gt top emit compaction_finished --actor ${esc(role)} --status "done" --message "${session}"`,
        );
        // Reset so next system.transform gets fresh context.
        primePromise = loadPrime();
      }
      if (event?.type === "session.deleted") {
        const sessionID = event.properties?.info?.id;
        if (sessionID) {
          await $`gt costs record --session ${sessionID}`.catch(() => {});
        }
      }
    },
    "experimental.chat.system.transform": async (input, output) => {
      // If session.created hasn't fired yet, start loading now.
      if (!primePromise) {
        primePromise = loadPrime();
      }
      const context = await primePromise;
      if (context) {
        output.system.push(context);
      } else {
        // Reset so next transform retries instead of pushing empty forever.
        primePromise = null;
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
      const session = await getSession();
      emit(
        `gt top emit compaction_started --actor ${esc(role)} --status "compacting" --message "${session}"`,
      );
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

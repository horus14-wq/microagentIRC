// mairc's chat delivery for Pi — both Pi Agents (the TypeScript original and the Rust port, which mirrors its
// extension API) share ~/.pi/agent and this file. After each tool call, and when a session starts, it runs
// `mairc hook` (which finds this agent from its process ancestry: Pi starts both the hook and mairc's MCP server)
// and adds what is waiting to what the model sees: to the tool's result, or, at session start, as a message for
// the next turn. Nothing waiting, an error, a slow daemon or an extension policy that denies running programs adds
// nothing; it never throws.
const MAIRC = __MAIRC_BINARY__;
const TIMEOUT_MS = 3000;

function sessionOf(ctx) {
  try {
    const id = ctx && ctx.sessionManager && ctx.sessionManager.getSessionId();
    return typeof id === "string" ? id : "";
  } catch {
    return "";
  }
}

// One `mairc hook` run; resolves to what it printed, trimmed, or "".
async function hook(pi, event, ctx) {
  const args = ["hook", "--client", "plain", "--event", event];
  const session = sessionOf(ctx);
  if (session) {
    args.push("--session", session);
  }
  try {
    const result = await pi.exec(MAIRC, args, { timeout: TIMEOUT_MS });
    return result && result.code === 0 ? String(result.stdout || "").trim() : "";
  } catch {
    return "";
  }
}

export default function (pi) {
  pi.on("tool_result", async (event, ctx) => {
    const lines = await hook(pi, "PostToolUse", ctx);
    if (!lines || !event || !Array.isArray(event.content)) {
      return null;
    }
    const patch = { content: [...event.content, { type: "text", text: lines }] };
    // The TypeScript Pi drops a result's structuredContent when a patch replaces content without returning it;
    // the lines only append a text part, so the structured data still matches and is passed through. The Rust
    // port ignores the extra field.
    if (event.structuredContent !== undefined) {
      patch.structuredContent = event.structuredContent;
    }
    return patch;
  });
  pi.on("session_start", async (_event, ctx) => {
    const lines = await hook(pi, "SessionStart", ctx);
    if (lines) {
      try {
        pi.sendMessage({ customType: "mairc", content: lines, display: true }, { deliverAs: "nextTurn" });
      } catch {
        // A message that cannot be queued is dropped, like any failed delivery.
      }
    }
    return null;
  });
}

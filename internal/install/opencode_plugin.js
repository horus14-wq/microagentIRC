// mairc's chat delivery for OpenCode v2. After a tool call, and at the first model request of a session, it runs
// `mairc hook` (which knows this agent's seat, name and session) and adds what is waiting to what the model sees:
// to the tool's result, or, at session start, to that request's system prompt. Nothing waiting, an error or a slow
// daemon adds nothing; it never blocks the agent past its timeout, throws or prints.
import { execFile } from "node:child_process";

const MAIRC = __MAIRC_BINARY__;
const TIMEOUT_MS = 3000;

// One `mairc hook` run with the hook JSON on stdin; resolves to what it printed, trimmed, or "".
function hook(event, facts) {
  return new Promise((resolve) => {
    try {
      const child = execFile(
        MAIRC,
        ["hook", "--client", "plain", "--event", event],
        { timeout: TIMEOUT_MS, windowsHide: true },
        (error, stdout) => resolve(error ? "" : String(stdout).trim()),
      );
      child.stdin.on("error", () => {});
      child.stdin.end(JSON.stringify({ hook_event_name: event, ...facts }));
    } catch {
      resolve("");
    }
  });
}

// mairc's own tools run inside OpenCode's code tool too; their results go to that code, not to the model, so they
// deliver nothing, except irc_name: its result names the agent, and the hook must see it to bind the session.
function deliversOn(tool) {
  return typeof tool === "string" && (!tool.startsWith("mairc_") || tool === "mairc_irc_name");
}

const started = new Set();

export default {
  id: "mairc",
  async setup(ctx) {
    await ctx.tool.hook("execute.after", async (event) => {
      const content = event && event.result && event.result.content;
      if (!Array.isArray(content) || !deliversOn(event.tool)) {
        return;
      }
      const lines = await hook("PostToolUse", {
        session_id: event.sessionID,
        tool_name: event.tool,
        tool_response: content,
      });
      if (lines) {
        content.push({ type: "text", text: lines });
      }
    });
    await ctx.session.hook("context", async (event) => {
      if (!event || typeof event.sessionID !== "string" || started.has(event.sessionID)) {
        return;
      }
      started.add(event.sessionID);
      const lines = await hook("SessionStart", { session_id: event.sessionID });
      if (lines && Array.isArray(event.system)) {
        event.system.push({ type: "text", text: lines });
      }
    });
  },
};

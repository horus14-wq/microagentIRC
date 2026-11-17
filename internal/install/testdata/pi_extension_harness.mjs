// Drives mairc's Pi extension (argv[2], an .mjs copy) with a fake `pi` whose exec runs the fake `mairc` the Go test
// supplies, and prints one JSON object describing what each event did.
import { pathToFileURL } from "node:url";
import { execFileSync } from "node:child_process";
import { writeFileSync, rmSync } from "node:fs";

const [, , extension, outFile, failFile] = process.argv;
const mod = await import(pathToFileURL(extension).href);
const handlers = {};
const messages = [];
const pi = {
  on: (name, cb) => { handlers[name] = cb; },
  sendMessage: (message, options) => { messages.push({ content: message.content, deliverAs: options.deliverAs }); },
  exec: async (cmd, args) => {
    try {
      return { code: 0, stdout: execFileSync(cmd, args, { encoding: "utf8" }) };
    } catch (error) {
      return { code: error.status ?? 1, stdout: String(error.stdout ?? "") };
    }
  },
};
mod.default(pi);
const ctx = { sessionManager: { getSessionId: () => "pi-session-1" } };
const report = { handlers: Object.keys(handlers).sort() };
const result = async () => {
  const out = await handlers.tool_result({ content: [{ type: "text", text: "tool output" }] }, ctx);
  return out === null ? null : out.content.map((part) => part.text);
};

writeFileSync(outFile, "irc:\npm alice: hi");
report.delivered = await result();
rmSync(outFile);
report.nothing = await result();
writeFileSync(failFile, "");
writeFileSync(outFile, "irc:\npm alice: lost");
report.failed = await result();
rmSync(failFile);
await handlers.session_start({}, ctx);
report.messages = messages;
console.log(JSON.stringify(report));

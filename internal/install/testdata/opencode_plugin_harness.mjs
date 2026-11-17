// Drives mairc's OpenCode plugin (argv[2], an .mjs copy) with a fake OpenCode context and prints one JSON object
// describing what each event did. The Go test supplies a fake `mairc` whose output and failure it controls.
import { pathToFileURL } from "node:url";
import { writeFileSync, rmSync } from "node:fs";

const [, , plugin, outFile, failFile] = process.argv;
const mod = await import(pathToFileURL(plugin).href);
const hooks = {};
const ctx = {
  tool: { hook: async (name, cb) => { hooks[`tool.${name}`] = cb; } },
  session: { hook: async (name, cb) => { hooks[`session.${name}`] = cb; } },
};
await mod.default.setup(ctx);

const report = { id: mod.default.id, hooks: Object.keys(hooks).sort() };
const after = async (tool) => {
  const event = { tool, sessionID: "ses_test1", result: { output: "x", content: [{ type: "text", text: "tool output" }] } };
  await hooks["tool.execute.after"](event);
  return event.result.content.map((part) => part.text);
};

writeFileSync(outFile, "irc:\npm alice: hi");
report.delivered = await after("bash");
report.inner = await after("mairc_irc_who");
report.named = await after("mairc_irc_name");
rmSync(outFile);
report.nothing = await after("bash");
writeFileSync(failFile, "");
writeFileSync(outFile, "irc:\npm alice: lost");
report.failed = await after("bash");
rmSync(failFile);

const context = { sessionID: "ses_test2", system: [{ type: "text", text: "base" }] };
await hooks["session.context"](context);
await hooks["session.context"](context);
report.system = context.system.map((part) => part.text);

console.log(JSON.stringify(report));

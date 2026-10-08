// Explicit source path keeps normal release builds independent of local repos.
const source = process.argv[2];
if (!source) throw new Error("Usage: bun run sync-agent-mark.ts /path/to/ui-kit/src/AgentMark.tsx");
const text = await Bun.file(source).text();
await Bun.write(`${import.meta.dir}/src/AgentMark.tsx`, "// Generated from @apteva/ui-kit/src/AgentMark.tsx; refresh with sync-agent-mark.ts.\n" + text);

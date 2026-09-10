import { transform } from "lightningcss";

/** One stylesheet for native panels, local imports and app-served embeds. */
export async function buildStyles(): Promise<string> {
  const process = Bun.spawn(["bunx", "--no-install", "tailwindcss", "-i", `${import.meta.dir}/styles.css`, "--minify"], {
    cwd: import.meta.dir, stdout: "pipe", stderr: "inherit",
  });
  const css = await new Response(process.stdout).arrayBuffer();
  if (await process.exited) throw new Error("Conversations stylesheet build failed");
  return transform({
    filename: "conversations.css", code: Buffer.from(css), minify: true,
    visitor: {
      Declaration(declaration) {
        // Inherit host radii. A self-referencing theme variable creates a cycle.
        if (declaration.property === "custom" && ["--radius-sm", "--radius-md", "--radius-lg"].includes(declaration.value.name)) return [];
      },
      Selector(selector) {
        if (selector.length === 1 && selector[0].type === "pseudo-class" && ["root", "host"].includes(selector[0].kind)) {
          return [{ type: "class", name: "apteva-conversations" }];
        }
        // Tailwind's fallback property initialization must not reset the host.
        if (selector.length === 1 && ["universal", "pseudo-element"].includes(selector[0].type)) {
          const scope = { type: "class" as const, name: "apteva-conversations" };
          const descendant = [scope, { type: "combinator" as const, value: "descendant" as const }, ...selector];
          return selector[0].type === "universal" ? [[scope], descendant] : descendant;
        }
      },
    },
  }).code.toString();
}

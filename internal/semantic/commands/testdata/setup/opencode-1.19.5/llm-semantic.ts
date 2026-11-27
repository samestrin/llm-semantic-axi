// Managed by `llm-semantic setup` for OpenCode 1.19.5 (@opencode-ai/plugin 1.19.5).
// Rerun `llm-semantic setup` to update this file; edits here are overwritten.
// The hook wait is bounded by Promise.race with a 5s timer that settles as an
// empty status, so a hanging command skips the prompt. The race bounds the wait
// but does not kill the shell child, which keeps running until it exits on its own.
import type { Plugin } from "@opencode-ai/plugin"

const command = "llm-semantic index-status || true"

export const LlmSemantic: Plugin = async ({ $, client, directory }) => ({
  event: async ({ event }) => {
    if (event.type !== "session.created" || event.properties.info.parentID) return
    const status = await Promise.race([
      $`sh -c ${command}`.cwd(directory).nothrow().quiet().text(),
      new Promise<string>((resolve) => setTimeout(() => resolve(""), 5000)),
    ])
    if (!status.trim()) return
    await client.session.prompt({
      path: { id: event.properties.info.id },
      body: { noReply: true, parts: [{ type: "text", text: status }] },
    })
  },
})

// Managed by `llm-semantic setup` for OpenCode 1.19.5 (@opencode-ai/plugin 1.19.5).
// Rerun `llm-semantic setup` to update this file; edits here are overwritten.
import type { Plugin } from "@opencode-ai/plugin"

const command = "llm-semantic index-status || true"

export const LlmSemantic: Plugin = async ({ $, client, directory }) => ({
  event: async ({ event }) => {
    if (event.type !== "session.created" || event.properties.info.parentID) return
    const status = await $`sh -c ${command}`.cwd(directory).nothrow().quiet().text()
    if (!status.trim()) return
    await client.session.prompt({
      path: { id: event.properties.info.id },
      body: { noReply: true, parts: [{ type: "text", text: status }] },
    })
  },
})

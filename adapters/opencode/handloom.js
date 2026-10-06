// handloom adapter for OpenCode. Managed by `handloom adapter install opencode`; edits are overwritten.
// It forwards OpenCode's events to `handloom hook opencode`, which reports the
// agent's state and, when the session goes idle, says whether new handloom mail
// must be handed over. It never sends message content anywhere.
import { spawn } from "node:child_process";

const HANDLOOM_BIN = {{handloom}};
const ENV = {{env}};

function hook(event, sessionID, directory) {
  return new Promise((resolve) => {
    let out = "";
    let done = false;
    const finish = () => {
      if (!done) {
        done = true;
        resolve(out);
      }
    };
    try {
      const child = spawn(HANDLOOM_BIN, ["hook", "opencode"], {
        stdio: ["pipe", "pipe", "ignore"],
        env: { ...process.env, ...ENV },
      });
      child.stdout.on("data", (d) => (out += d));
      child.on("error", finish);
      child.on("close", finish);
      const timer = setTimeout(() => {
        try {
          child.kill();
        } catch {}
        finish();
      }, 15000);
      timer.unref?.();
      child.stdin.end(JSON.stringify({ hook_event_name: event, session_id: sessionID ?? "", cwd: directory }));
    } catch {
      finish();
    }
  });
}

export const HandloomPlugin = async ({ client, directory }) => {
  let current = "";
  return {
    "chat.message": async ({ sessionID }) => {
      current = sessionID ?? current;
      await hook("UserPromptSubmit", current, directory);
    },
    "tool.execute.after": async () => {
      void hook("PostToolUse", current, directory);
    },
    event: async ({ event }) => {
      const id = event?.properties?.sessionID ?? event?.properties?.info?.id ?? current;
      switch (event?.type) {
        case "session.created":
          current = id;
          await hook("SessionStart", id, directory);
          break;
        case "permission.asked":
          await hook("PermissionRequest", id, directory);
          break;
        case "session.idle": {
          const out = await hook("Stop", id, directory);
          try {
            const d = JSON.parse(out);
            if (d?.decision === "block" && d.reason && id) {
              await client.session.prompt({ path: { id }, body: { parts: [{ type: "text", text: d.reason }] } });
            }
          } catch {}
          break;
        }
        case "session.deleted":
          await hook("SessionEnd", id, directory);
          break;
      }
    },
  };
};

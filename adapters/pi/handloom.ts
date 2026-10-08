// handloom adapter for Pi. Managed by `handloom adapter install pi`; edits are overwritten.
// It forwards Pi's lifecycle events to `handloom hook pi`, which reports the
// agent's state and, at the end of a turn, says whether new handloom mail must
// be handed over. It never sends message content anywhere.
// @ts-nocheck
import { spawn } from "node:child_process";

const HANDLOOM = {{handloom}};
const KIND = "{{kind}}";
const ENV = {{env}};

function sessionId(ctx) {
  try {
    return ctx?.sessionManager?.getSessionId?.() ?? "";
  } catch {
    return "";
  }
}

function hook(event, ctx) {
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
      const child = spawn(HANDLOOM, ["hook", KIND], {
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
      child.stdin.end(
        JSON.stringify({ hook_event_name: event, session_id: sessionId(ctx), cwd: ctx?.cwd ?? process.cwd() }),
      );
    } catch {
      finish();
    }
  });
}

// turnEnd asks handloom whether the agent may stop. If mail arrived during the
// turn, handloom answers with a fixed sentence and the agent gets it as its next
// user message.
async function turnEnd(pi, ctx) {
  const out = await hook("Stop", ctx);
  try {
    const d = JSON.parse(out);
    if (d?.decision === "block" && d.reason) {
      pi.sendUserMessage(d.reason, { deliverAs: "followUp" });
    }
  } catch {}
}

export default function (pi) {
  pi.on("session_start", async (_event, ctx) => {
    await hook("SessionStart", ctx);
  });
  pi.on("agent_start", async (_event, ctx) => {
    await hook("UserPromptSubmit", ctx);
  });
  pi.on("tool_execution_end", (_event, ctx) => {
    void hook("PostToolUse", ctx);
  });
  pi.on("{{stop_event}}", async (_event, ctx) => {
    await turnEnd(pi, ctx);
  });
{{extra}}  pi.on("session_shutdown", async (_event, ctx) => {
    await hook("SessionEnd", ctx);
  });
}

// tmux-agent-herd: reports OpenCode's state to herd so tmux can show it.
// Installed by `herd setup opencode`; safe to delete. Does nothing outside tmux.
import { execFile } from "node:child_process";

const HOOK = __HERD_HOOK_JSON__;

function report(state, msg, kind) {
  const args = ["--agent", "opencode", state];
  if (msg) args.push("--msg", String(msg));
  if (kind) args.push("--kind", kind);
  execFile(HOOK, args, { timeout: 3000 }, () => {});
}

export default {
  id: "tmux-agent-herd",
  async setup(ctx) {
    if (!process.env.TMUX_PANE) return;
    const controller = new AbortController();
    (async () => {
      for await (const ev of ctx.event.subscribe({ signal: controller.signal })) {
        const d = ev?.data ?? {};
        switch (ev?.type) {
          case "session.execution.started":
          case "permission.replied":
          case "form.replied":
          case "form.cancelled":
            report("working");
            break;
          case "permission.asked":
            report("blocked", d.message || [d.action, ...(d.resources ?? [])].filter(Boolean).join(" "), "permission");
            break;
          case "form.created":
            report("blocked", d.form?.title, "question");
            break;
          case "session.execution.succeeded":
            report("done");
            break;
          case "session.execution.failed":
            report("error");
            break;
          case "session.execution.interrupted":
            report("idle");
            break;
        }
      }
    })().catch(() => {});
    return () => {
      controller.abort();
      report("none");
    };
  },
};

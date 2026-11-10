// tmux-agent-herd: reports pi's state to herd so tmux can show it.
// Installed by `herd setup pi`; safe to delete. Does nothing outside tmux.
const HOOK: string = __HERD_HOOK_JSON__;

export default function (pi: any) {
  if (!process.env.TMUX_PANE) return;
  let running = false;
  let lastError = "";
  const report = (state: string, msg?: string, kind?: string) => {
    const args = ["--agent", "pi", state];
    if (msg) args.push("--msg", String(msg));
    if (kind) args.push("--kind", kind);
    Promise.resolve(pi.exec(HOOK, args, { timeout: 3000 })).catch(() => {});
  };
  pi.on("session_start", async () => report("idle"));
  pi.on("agent_start", async () => {
    running = true;
    lastError = "";
    report("working");
  });
  pi.on("agent_end", async (e: any) => {
    const msgs = Array.isArray(e?.messages) ? e.messages : [];
    const last = [...msgs].reverse().find((m: any) => m?.role === "assistant");
    lastError = last?.stopReason === "error" ? String(last.errorMessage || "error") : "";
  });
  pi.on("tool_execution_start", async (e: any) => report("working", e?.toolName));
  pi.on("ui_prompt_start", async (e: any) => report("blocked", e?.title, "question"));
  pi.on("ui_prompt_end", async () => report(running ? "working" : "idle"));
  pi.on("agent_settled", async (e: any) => {
    running = false;
    if (e?.aborted) report("idle");
    else if (lastError) report("error", lastError.slice(0, 200));
    else report("done");
  });
  pi.on("session_shutdown", async () => report("none"));
}

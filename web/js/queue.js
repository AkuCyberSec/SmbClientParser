// Queuing a run (used by the Runs and Results tabs).
// The server checks the settings first and returns warnings (no ruler file, unsaved changes,
// no enabled rules): the user decides whether to queue anyway.
import { api } from "./api.js";
import { confirmAction, el, sentence, showToast } from "./ui.js";

/** Resolves with the queued run, or null if the user went back. Errors are thrown (e.g. input file not found). */
export async function queueRun(settings) {
  const { warnings } = await api.post("/api/runs/check", settings);
  if (warnings.length > 0) {
    const queueAnyway = await confirmAction({
      title: "Queue the run anyway?",
      message: [el("ul", { class: "warning-list" }, warnings.map((warning) => el("li", {}, sentence(warning))))],
      confirmText: "Queue anyway",
      cancelText: "Go back",
    });
    if (!queueAnyway) return null;
  }
  const run = await api.post("/api/runs", settings);
  showToast(`Run #${run.id} added to the queue`);
  return run;
}

// Runs tab: the "New run" form and the queue/history of runs.
import { api, withQuery } from "./api.js";
import * as resultsTab from "./results.js";
import { uploadWithConfirmation } from "./upload.js";
import {
  button,
  el,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatSize,
  openDialog,
  openTab,
  replaceKeepingFocus,
  sentence,
  showError,
  showToast,
} from "./ui.js";

// How each run status is shown: text and badge color
const STATUS_DISPLAY = {
  queued: { text: "Queued", badge: "" },
  running: { text: "Running", badge: "info" },
  cancelling: { text: "Cancel requested", badge: "warning" },
  done: { text: "Done", badge: "success" },
  failed: { text: "Failed", badge: "danger" },
  cancelled: { text: "Cancelled", badge: "warning" },
};

const form = document.getElementById("run-form");
const inputField = document.getElementById("run-input");
const serverField = document.getElementById("run-server");
const shareField = document.getElementById("run-share");
const delimiterField = document.getElementById("run-delimiter");
const formError = document.getElementById("run-form-error");
const runList = document.getElementById("run-list");
const runListEmpty = document.getElementById("run-list-empty");

// The delimiter follows the ruler setting until the user picks one for the run
let delimiterChosenByUser = false;

export function init(config) {
  inputField.value = config.default_input_file;
  serverField.value = config.default_server;
  shareField.value = config.default_share;

  form.addEventListener("submit", submitRun);
  delimiterField.addEventListener("change", () => (delimiterChosenByUser = true));
  document.getElementById("run-pick-upload").addEventListener("click", pickFromUploads);

  const fileInput = document.getElementById("run-upload-input");
  document.getElementById("run-upload").addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", async () => {
    const file = fileInput.files[0];
    fileInput.value = ""; // allows choosing the same file again
    if (!file) return;
    const uploaded = await uploadWithConfirmation(file, document.getElementById("run-upload-progress"));
    if (uploaded) useInputFile(uploaded.path);
  });
}

export async function show() {
  if (!delimiterChosenByUser) {
    try {
      const ruler = await api.get("/api/rules");
      delimiterField.value = ruler.path_delimiter;
    } catch (error) {
      showError(error);
    }
  }
}

export function refresh(runs) {
  renderRunList(runs);
}

/** Sets the input file of the form (also used by the Files tab). */
export function useInputFile(path) {
  inputField.value = path;
  inputField.removeAttribute("aria-invalid");
}

async function submitRun(event) {
  event.preventDefault();
  formError.textContent = "";

  const requiredFields = [
    [inputField, "Choose the input file (smbclient output)."],
    [serverField, "Enter the server name."],
    [shareField, "Enter the share name."],
  ];
  for (const [field, message] of requiredFields) {
    const isEmpty = field.value.trim() === "";
    field.setAttribute("aria-invalid", String(isEmpty));
    if (isEmpty) {
      formError.textContent = message;
      field.focus();
      return;
    }
  }

  const submitButton = form.querySelector('button[type="submit"]');
  submitButton.disabled = true;
  try {
    const run = await api.post("/api/runs", {
      input_file: inputField.value.trim(),
      server: serverField.value.trim(),
      share: shareField.value.trim(),
      path_delimiter: delimiterField.value,
    });
    showToast(`Run #${run.id} added to the queue`);
    const { runs } = await api.get("/api/runs");
    renderRunList(runs);
  } catch (error) {
    formError.textContent = sentence(error.message);
  } finally {
    submitButton.disabled = false;
  }
}

/** Shows the files of the upload folder and puts the chosen one in the form. */
async function pickFromUploads() {
  let listing;
  try {
    listing = await api.get(withQuery("/api/files", { folder: "uploads" }));
  } catch (error) {
    return showError(error);
  }

  const intro = el("p", {}, "Files in ", el("span", { class: "path" }, listing.path), ":");
  if (listing.files.length === 0) {
    await openDialog({
      title: "Choose from uploads",
      body: [intro, el("p", { class: "muted" }, "No files yet. Upload one, or copy files into the folder above.")],
      buttons: [],
      cancelText: "Close",
    });
    return;
  }

  // One radio button per file: keyboard friendly (arrow keys) and the first one is preselected
  const options = listing.files.map((file, index) =>
    el(
      "label",
      { class: "row" },
      el("input", { type: "radio", name: "upload-choice", value: file.path, checked: index === 0 }),
      el("span", { class: "grow path" }, file.name),
      el("span", { class: "muted small" }, `${formatSize(file.size)} · ${formatDateTime(file.modified)}`),
    ),
  );
  const choice = await openDialog({
    title: "Choose from uploads",
    body: [intro, el("div", { class: "stack" }, options)],
    buttons: [{ text: "Use this file", value: "use", kind: "primary" }],
  });
  if (choice !== "use") return;
  const selected = options.map((option) => option.querySelector("input")).find((radio) => radio.checked);
  if (selected) useInputFile(selected.value);
}

function renderRunList(runs) {
  runListEmpty.hidden = runs.length > 0;
  replaceKeepingFocus(runList, ...runs.map(renderRun));
}

function renderRun(run) {
  const status = STATUS_DISPLAY[run.status];
  const actions = [];
  if (run.status === "queued" || run.status === "running") {
    actions.push(button({ text: "Cancel", iconName: "x", small: true, focusKey: `cancel-${run.id}`, onClick: () => cancelRun(run) }));
  }
  if (run.has_results) {
    actions.push(
      button({
        text: "View results",
        iconName: "eye",
        small: true,
        focusKey: `view-${run.id}`,
        onClick: () => {
          resultsTab.selectRun(run.id);
          openTab("results");
        },
      }),
    );
  }

  return el(
    "li",
    { class: "run-item" },
    el(
      "div",
      { class: "row" },
      el("strong", {}, `#${run.id}`),
      el("span", { class: "grow" }, `${run.settings.server} / ${run.settings.share}`),
      el("span", { class: `badge ${status.badge}` }, status.text),
      actions,
    ),
    el("span", { class: "path muted" }, run.settings.input_file),
    run.status === "running" || run.status === "cancelling" ? renderProgress(run.progress) : null,
    el("span", { class: "small muted" }, describeTimes(run)),
    run.error ? el("span", { class: "field-error" }, sentence(run.error)) : null,
    run.results_dropped ? el("span", { class: "small muted" }, "Results freed from memory (only the newest runs keep them, see -keep-runs).") : null,
  );
}

function renderProgress(percent) {
  const bar = el("div", { class: "progress-bar" });
  bar.style.width = `${percent}%`;
  return el(
    "div",
    { class: "row" },
    el("div", { class: "progress grow", role: "progressbar", "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": String(Math.floor(percent)), "aria-label": "Parsing progress" }, bar),
    el("span", { class: "small" }, `${Math.floor(percent)}%`),
  );
}

function describeTimes(run) {
  const parts = [`Queued ${formatDateTime(run.queued_at)}`];
  if (run.started_at) parts.push(`started ${formatDateTime(run.started_at)}`);
  if (run.started_at && run.finished_at) parts.push(`took ${formatDuration(new Date(run.finished_at) - new Date(run.started_at))}`);
  if (run.status === "done") parts.push(`${formatNumber(run.matches)} matches`);
  return parts.join(" · ");
}

async function cancelRun(run) {
  try {
    await api.post("/api/runs/cancel", { id: run.id });
    showToast(run.status === "queued" ? `Run #${run.id} removed from the queue` : `Cancel requested for run #${run.id}`);
    const { runs } = await api.get("/api/runs");
    renderRunList(runs);
  } catch (error) {
    showError(error);
  }
}

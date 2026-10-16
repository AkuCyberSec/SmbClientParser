// Runs tab: the "New run" form and the queue/history of runs.
import { api, withQuery } from "./api.js";
import { deleteRuns, isFinished } from "./delete-runs.js";
import { queueRun } from "./queue.js";
import * as resultsTab from "./results.js";
import { uploadWithConfirmation } from "./upload.js";
import {
  button,
  dropdownMenu,
  el,
  formatDateTime,
  formatDuration,
  formatNumber,
  formatSize,
  openDialog,
  openTab,
  pathText,
  replaceKeepingFocus,
  sentence,
  showError,
  showToast,
  startBrowserDownload,
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

const EXPORT_FORMATS = ["txt", "csv", "json"];

// Value of the Rules selector meaning "the rules of the Rules tab" (other values are file names in the upload folder)
const RULES_TAB = "";

const form = document.getElementById("run-form");
const inputField = document.getElementById("run-input");
const rulerSelect = document.getElementById("run-ruler");
const rulerHint = document.getElementById("run-ruler-hint");
const serverField = document.getElementById("run-server");
const shareField = document.getElementById("run-share");
const delimiterField = document.getElementById("run-delimiter");
const formError = document.getElementById("run-form-error");
const runList = document.getElementById("run-list");
const runListEmpty = document.getElementById("run-list-empty");
const queueTitle = document.getElementById("queue-title");
const deleteSelectedButton = document.getElementById("runs-delete-selected");
const deleteFinishedButton = document.getElementById("runs-delete-finished");
const selectedCountBadge = document.getElementById("runs-selected-count");

// The delimiter follows the chosen rules until the user picks one for the run
let delimiterChosenByUser = false;
let rulesTab = null; // { path, dirty, path_delimiter } of the Rules tab
let uploadedRulers = []; // .json files of the upload folder (see uploadedRuler in webrules.go)
let latestRuns = []; // newest first
const selectedRunIds = new Set(); // finished runs ticked for "Delete selected"

export function init(config) {
  inputField.value = config.default_input_file;
  serverField.value = config.default_server;
  shareField.value = config.default_share;

  form.addEventListener("submit", submitRun);
  delimiterField.addEventListener("change", () => (delimiterChosenByUser = true));
  rulerSelect.addEventListener("change", rulerChanged);
  document.getElementById("run-pick-upload").addEventListener("click", pickFromUploads);
  deleteSelectedButton.addEventListener("click", () => deleteAndRefresh(latestRuns.filter((run) => selectedRunIds.has(run.id))));
  deleteFinishedButton.addEventListener("click", () => deleteAndRefresh(latestRuns.filter(isFinished)));

  const fileInput = document.getElementById("run-upload-input");
  document.getElementById("run-upload").addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", async () => {
    const file = fileInput.files[0];
    fileInput.value = ""; // allows choosing the same file again
    if (!file) return;
    const uploaded = await uploadWithConfirmation(file, document.getElementById("run-upload-progress"));
    if (uploaded) useInputFile(uploaded.path);
    loadRulerChoices(); // the upload may be a ruler file
  });
}

export function show() {
  return loadRulerChoices();
}

export function refresh(runs) {
  renderRunList(runs);
}

/** Sets the input file of the form (also used by the Files tab). */
export function useInputFile(path) {
  inputField.value = path;
  inputField.removeAttribute("aria-invalid");
}

// ---------------------------------------------------------------------------
// Rules selector: the Rules tab or a ruler file of the upload folder
// ---------------------------------------------------------------------------

async function loadRulerChoices() {
  try {
    const [ruler, uploaded] = await Promise.all([api.get("/api/rules"), api.get("/api/rules/uploaded")]);
    rulesTab = ruler;
    uploadedRulers = uploaded.rulers;
    renderRulerSelect();
  } catch (error) {
    showError(error);
  }
}

function renderRulerSelect() {
  const selected = rulerSelect.value;
  rulerSelect.replaceChildren(
    el("option", { value: RULES_TAB }, "Rules tab (current rules)"),
    ...uploadedRulers.map((ruler) =>
      el("option", { value: ruler.name, disabled: ruler.error !== undefined }, ruler.error ? `uploads/${ruler.name} (not usable)` : `uploads/${ruler.name}`),
    ),
  );
  // Keep the previous choice if it's still available
  const stillAvailable = uploadedRulers.some((ruler) => ruler.name === selected && !ruler.error);
  rulerSelect.value = stillAvailable ? selected : RULES_TAB;
  rulerChanged();
}

/** Shows where the chosen rules come from and applies their path delimiter. */
function rulerChanged() {
  const uploaded = uploadedRulers.find((ruler) => ruler.name === rulerSelect.value);
  if (uploaded) {
    rulerHint.textContent = uploaded.path;
  } else if (rulesTab.path === "") {
    rulerHint.textContent = "Not saved to a file";
  } else {
    rulerHint.textContent = rulesTab.dirty ? `${rulesTab.path} (with unsaved changes)` : rulesTab.path;
  }
  if (!delimiterChosenByUser) delimiterField.value = uploaded ? uploaded.path_delimiter : rulesTab.path_delimiter;
}

// ---------------------------------------------------------------------------
// New run
// ---------------------------------------------------------------------------

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
    const run = await queueRun({
      input_file: inputField.value.trim(),
      server: serverField.value.trim(),
      share: shareField.value.trim(),
      path_delimiter: delimiterField.value,
      ruler_file: rulerSelect.value,
    });
    if (run) {
      const { runs } = await api.get("/api/runs");
      renderRunList(runs);
    }
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

// ---------------------------------------------------------------------------
// Queue and history
// ---------------------------------------------------------------------------

function renderRunList(runs) {
  latestRuns = runs;
  // Forget ticked runs that no longer exist (e.g. deleted from another browser tab)
  for (const id of selectedRunIds) {
    if (!runs.some((run) => run.id === id)) selectedRunIds.delete(id);
  }
  updateDeleteButtons();

  // Redrawing would close a menu the user has open: wait for the next refresh
  if (runList.querySelector("details.menu[open]")) return;
  runListEmpty.hidden = runs.length > 0;
  replaceKeepingFocus(runList, ...runs.map(renderRun));
}

function updateDeleteButtons() {
  selectedCountBadge.textContent = String(selectedRunIds.size);
  selectedCountBadge.hidden = selectedRunIds.size === 0;
  deleteSelectedButton.disabled = selectedRunIds.size === 0;
  deleteFinishedButton.disabled = !latestRuns.some(isFinished);
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
      dropdownMenu({
        text: "Download",
        iconName: "download",
        label: `Download the results of run #${run.id}`,
        focusKey: `download-${run.id}`,
        items: EXPORT_FORMATS.map((format) => ({ text: format.toUpperCase(), iconName: "file", onClick: () => quickExport(run, format) })),
      }),
    );
  }
  if (isFinished(run)) {
    actions.push(button({ iconName: "trash", kind: "ghost", small: true, label: `Delete run #${run.id}`, focusKey: `delete-${run.id}`, onClick: () => deleteAndRefresh([run]) }));
  }

  return el(
    "li",
    { class: "run-item" },
    el(
      "div",
      { class: "row" },
      renderSelectBox(run),
      el("strong", {}, `#${run.id}`),
      el("span", { class: "grow" }, `${run.settings.server} / ${run.settings.share}`),
      el("span", { class: `badge ${status.badge}` }, status.text),
      actions,
    ),
    el("span", { class: "path muted" }, run.settings.input_file),
    el("span", { class: "small muted" }, "Rules: ", el("span", { class: "path" }, run.ruler_source)),
    run.status === "running" || run.status === "cancelling" ? renderProgress(run.progress) : null,
    el("span", { class: "small muted" }, describeTimes(run)),
    run.error ? el("span", { class: "field-error" }, sentence(run.error)) : null,
    run.results_dropped ? el("span", { class: "small muted" }, "Results freed from memory (only the newest runs keep them, see -keep-runs).") : null,
  );
}

/** Checkbox for "Delete selected". Queued and running runs can't be ticked. */
function renderSelectBox(run) {
  const finished = isFinished(run);
  return el(
    "label",
    { class: "check-area", title: finished ? `Select run #${run.id}` : "Queued and running runs can't be deleted: cancel them first" },
    el("input", {
      type: "checkbox",
      "aria-label": `Select run #${run.id}`,
      "data-focus-key": `select-${run.id}`,
      checked: selectedRunIds.has(run.id),
      disabled: !finished,
      onchange: (event) => {
        if (event.target.checked) selectedRunIds.add(run.id);
        else selectedRunIds.delete(run.id);
        updateDeleteButtons();
      },
    }),
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

/** Saves the results in the download folder with a unique name, then downloads them in the browser. */
async function quickExport(run, format) {
  try {
    const saved = await api.post("/api/runs/export", { id: run.id, format, file_name: resultsTab.resultsFileName(run, format), overwrite: false });
    startBrowserDownload(saved.folder, saved.name);
    showToast(pathText("Saved to", saved.path));
  } catch (error) {
    showError(error);
  }
}

async function deleteAndRefresh(runs) {
  if (runs.length === 0 || !(await deleteRuns(runs))) return;
  for (const run of runs) selectedRunIds.delete(run.id);
  try {
    const { runs: current } = await api.get("/api/runs");
    renderRunList(current);
  } catch (error) {
    showError(error);
  }
  // The Delete button used may be gone with its run, or disabled: keep keyboard users near the list
  const focused = document.activeElement;
  if (focused === document.body || focused.disabled) queueTitle.focus();
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

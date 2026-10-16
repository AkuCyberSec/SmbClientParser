// Deleting finished runs, used by the Runs and Results tabs:
// from memory (results freed, the runs leave Queue and history), from disk (their exported files), or both.
import { api, withQuery } from "./api.js";
import { el, formatDateTime, formatNumber, formatSize, openDialog, pathText, showError, showToast } from "./ui.js";

const FINISHED_STATUSES = ["done", "failed", "cancelled"];

// Values of the "What do you want to delete?" choices
const DELETE_MEMORY = "memory";
const DELETE_DISK = "disk";
const DELETE_BOTH = "both";

const DIALOG_DELETE = "delete";

// Each choice explains what freeing means
const CHOICES = [
  {
    value: DELETE_MEMORY,
    title: "Memory only",
    description:
      "Frees the server's memory. The runs leave Queue and history: their results can't be viewed, filtered or exported anymore, and the copy of the rules they used is lost. To see them again, queue a new run. The memory goes back to the operating system right away.",
    needsFiles: false,
  },
  {
    value: DELETE_DISK,
    title: "Disk only",
    description: "Frees disk space. The ticked exported files are deleted from the download folder for good. The runs and their results stay in memory.",
    needsFiles: true,
  },
  {
    value: DELETE_BOTH,
    title: "Memory and disk",
    description: "Frees memory and disk space: both of the above.",
    needsFiles: true,
  },
];

/** Queued and running runs can't be deleted: they must be cancelled first. */
export function isFinished(run) {
  return FINISHED_STATUSES.includes(run.status);
}

/**
 * Asks what to delete (memory, disk or both) for the given finished runs, then deletes it.
 * Resolves with true if anything was deleted.
 */
export async function deleteRuns(runs) {
  let downloads;
  try {
    downloads = await api.get(withQuery("/api/files", { folder: "downloads" }));
  } catch (error) {
    showError(error);
    return false;
  }
  // Exports of these runs still in the download folder (files renamed or deleted in the meantime are left out)
  const owners = new Map(runs.flatMap((run) => run.exports.map((name) => [name, run.id])));
  const exportedFiles = downloads.files.filter((file) => owners.has(file.name));

  const fileBox = renderFileBox(exportedFiles, owners, downloads.path);
  const choiceBox = renderChoiceBox(exportedFiles.length > 0, (choice) => (fileBox.element.hidden = choice === DELETE_MEMORY));
  fileBox.element.hidden = true;

  const confirmed = await openDialog({
    title: runs.length === 1 ? `Delete run #${runs[0].id}?` : `Delete ${runs.length} runs?`,
    body: [
      renderRunList(runs),
      choiceBox.element,
      fileBox.element,
      el("p", { class: "small muted" }, "Never deleted: input files, ruler files, files your browser already downloaded, and the Activity log (it records what gets deleted)."),
    ],
    buttons: [{ text: "Delete", value: DIALOG_DELETE, kind: "danger" }],
    validate: () => {
      const choice = choiceBox.selected();
      if (!choice) return "choose what to delete";
      if (choice !== DELETE_MEMORY && fileBox.ticked().length === 0) return "tick at least one file to delete from disk";
      return "";
    },
  });
  if (confirmed !== DIALOG_DELETE) return false;

  const choice = choiceBox.selected();
  try {
    const result = await api.post("/api/runs/delete", {
      ids: runs.map((run) => run.id),
      from_memory: choice !== DELETE_DISK,
      export_files: choice === DELETE_MEMORY ? [] : fileBox.ticked(),
    });
    reportResult(result, downloads.path);
    return result.deleted_runs.length > 0 || result.deleted_files.length > 0;
  } catch (error) {
    showError(error);
    return false;
  }
}

/** The runs about to be deleted, with their input file (which is never deleted). */
function renderRunList(runs) {
  return el(
    "ul",
    { class: "dialog-list" },
    runs.map((run) =>
      el(
        "li",
        {},
        el("span", {}, el("strong", {}, `#${run.id}`), ` ${run.settings.server} / ${run.settings.share} · ${describeResults(run)}`),
        el("span", { class: "path muted" }, run.settings.input_file),
      ),
    ),
  );
}

function describeResults(run) {
  if (run.has_results) return `${formatNumber(run.matches)} matches in memory`;
  if (run.results_dropped) return "results already freed";
  return `${run.status}, no results`;
}

/** Radio buttons for memory / disk / both. Without exported files only memory can be chosen. */
function renderChoiceBox(hasFiles, onChange) {
  const radios = CHOICES.map((choice) =>
    el("input", {
      type: "radio",
      name: "delete-choice",
      value: choice.value,
      disabled: choice.needsFiles && !hasFiles,
      checked: !hasFiles && choice.value === DELETE_MEMORY,
      onchange: () => onChange(choice.value),
    }),
  );
  const element = el(
    "fieldset",
    { class: "stack" },
    el("legend", {}, "What do you want to delete?"),
    CHOICES.map((choice, index) =>
      el(
        "label",
        { class: "choice" },
        radios[index],
        el("span", { class: "choice-text" }, el("strong", {}, choice.title), el("span", { class: "small muted" }, choice.description)),
      ),
    ),
    hasFiles ? null : el("p", { class: "field-hint" }, "No exported files of these runs are in the download folder: only memory can be freed."),
  );
  return { element, selected: () => radios.find((radio) => radio.checked)?.value };
}

/** Checkboxes for the exported files, all ticked, with the disk space they take. */
function renderFileBox(files, owners, folderPath) {
  const checkboxes = files.map((file) => el("input", { type: "checkbox", value: file.name, checked: true, onchange: updateTotal }));
  const total = el("p", { class: "small muted", "aria-live": "polite" });
  const element = el(
    "fieldset",
    { class: "stack" },
    el("legend", {}, pathText("Exported files to delete from", folderPath)),
    el(
      "ul",
      { class: "dialog-list" },
      files.map((file, index) =>
        el(
          "li",
          {},
          el(
            "label",
            { class: "check-row", title: file.path },
            checkboxes[index],
            el("span", { class: "grow path" }, file.name),
            el("span", { class: "small muted nowrap" }, `run #${owners.get(file.name)} · ${formatSize(file.size)} · ${formatDateTime(file.modified)}`),
          ),
        ),
      ),
    ),
    total,
  );

  function ticked() {
    return checkboxes.filter((checkbox) => checkbox.checked).map((checkbox) => checkbox.value);
  }
  function updateTotal() {
    const tickedNames = ticked();
    const bytes = files.filter((file) => tickedNames.includes(file.name)).reduce((sum, file) => sum + file.size, 0);
    total.textContent = `Disk space freed: ${formatSize(bytes)}`;
  }
  updateTotal();
  return { element, ticked };
}

function reportResult(result, folderPath) {
  const runIds = result.deleted_runs.map((id) => `#${id}`).join(", ");
  if (result.deleted_runs.length === 1) showToast(`Run ${runIds} deleted from memory`);
  if (result.deleted_runs.length > 1) showToast(`Runs ${runIds} deleted from memory`);

  if (result.deleted_files.length === 1) showToast(pathText("Deleted", result.deleted_files[0]));
  if (result.deleted_files.length > 1) showToast(pathText(`Deleted ${result.deleted_files.length} files from`, folderPath));

  for (const message of result.errors) showToast(message, "error");
}

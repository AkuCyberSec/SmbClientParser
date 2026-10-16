// Files tab: the upload and download folders (list, upload, download, rename, delete).
// Only files directly inside each folder are shown: no subfolders, no parent folders.
import { api, ApiError, withQuery } from "./api.js";
import * as runsTab from "./runs.js";
import { uploadWithConfirmation } from "./upload.js";
import {
  askText,
  button,
  confirmAction,
  el,
  formatDateTime,
  formatSize,
  openTab,
  pathMessage,
  pathText,
  showError,
  showToast,
  startBrowserDownload,
} from "./ui.js";

const UPLOADS = "uploads";
const DOWNLOADS = "downloads";

export function init(config) {
  document.getElementById("uploads-hint").replaceChildren(
    pathMessage("Upload a file here, or copy files into", config.upload_folder, " and press Refresh. Uploaded files can be used as input of a run."),
  );
  document.getElementById("downloads-hint").replaceChildren(pathMessage("Exported results and rules are saved in", config.download_folder));

  document.getElementById("uploads-refresh").addEventListener("click", () => loadFolder(UPLOADS));
  document.getElementById("downloads-refresh").addEventListener("click", () => loadFolder(DOWNLOADS));

  const fileInput = document.getElementById("uploads-upload-input");
  document.getElementById("uploads-upload").addEventListener("click", () => fileInput.click());
  fileInput.addEventListener("change", async () => {
    const file = fileInput.files[0];
    fileInput.value = ""; // allows choosing the same file again
    if (!file) return;
    if (await uploadWithConfirmation(file, document.getElementById("uploads-progress"))) loadFolder(UPLOADS);
  });
}

export function show() {
  loadFolder(UPLOADS);
  loadFolder(DOWNLOADS);
}

/** Files change only on user actions (or when copied by hand: use Refresh). */
export function refresh() {}

async function loadFolder(folder) {
  try {
    const listing = await api.get(withQuery("/api/files", { folder }));
    document.getElementById(`${folder}-files`).replaceChildren(renderTable(folder, listing.files));
  } catch (error) {
    showError(error);
  }
}

function renderTable(folder, files) {
  if (files.length === 0) {
    return el("p", { class: "empty-state" }, folder === UPLOADS ? "No uploaded files yet." : "No exported files yet.");
  }
  return el(
    "table",
    { class: "table" },
    el("caption", { class: "visually-hidden" }, folder === UPLOADS ? "Uploaded files" : "Exported files"),
    el(
      "thead",
      {},
      el(
        "tr",
        {},
        el("th", { scope: "col" }, "Name"),
        el("th", { scope: "col", class: "number" }, "Size"),
        el("th", { scope: "col" }, "Modified"),
        el("th", { scope: "col", class: "actions" }, el("span", { class: "visually-hidden" }, "Actions")),
      ),
    ),
    el("tbody", {}, files.map((file) => renderFileRow(folder, file))),
  );
}

function renderFileRow(folder, file) {
  const actions = [];
  if (folder === UPLOADS) {
    actions.push(
      button({
        text: "Use as input",
        iconName: "play",
        small: true,
        onClick: () => {
          runsTab.useInputFile(file.path);
          openTab("runs");
          showToast(pathText("Input file set to", file.path));
        },
      }),
    );
  }
  actions.push(
    button({ iconName: "download", kind: "ghost", small: true, label: `Download ${file.name}`, onClick: () => startBrowserDownload(folder, file.name) }),
    button({ iconName: "pencil", kind: "ghost", small: true, label: `Rename ${file.name}`, onClick: () => renameFile(folder, file) }),
    button({ iconName: "trash", kind: "ghost", small: true, label: `Delete ${file.name}`, onClick: () => deleteFile(folder, file) }),
  );

  return el(
    "tr",
    {},
    el("td", { class: "path", title: file.path }, file.name),
    el("td", { class: "number" }, formatSize(file.size)),
    el("td", { class: "nowrap" }, formatDateTime(file.modified)),
    el("td", { class: "actions" }, el("span", { class: "row" }, actions)),
  );
}

async function renameFile(folder, file) {
  const newName = await askText({
    title: "Rename file",
    message: pathMessage("File:", file.path),
    label: "New name",
    value: file.name,
    confirmText: "Rename",
    mono: true,
  });
  if (newName === null || newName === file.name) return;

  const rename = (overwrite) => api.post("/api/files/rename", { folder, name: file.name, new_name: newName, overwrite });
  try {
    await rename(false);
  } catch (error) {
    if (!(error instanceof ApiError && error.isFileExists)) return showError(error);
    const replace = await confirmAction({
      title: "Replace existing file?",
      message: pathMessage("This file already exists:", error.data.path, ". Replace it?"),
      confirmText: "Replace",
      danger: true,
    });
    if (!replace) return;
    try {
      await rename(true);
    } catch (retryError) {
      return showError(retryError);
    }
  }
  showToast(`Renamed to ${newName}`);
  loadFolder(folder);
}

async function deleteFile(folder, file) {
  const confirmed = await confirmAction({
    title: "Delete file?",
    message: pathMessage("This file will be deleted from the server:", file.path, ". This can't be undone."),
    confirmText: "Delete file",
    danger: true,
  });
  if (!confirmed) return;
  try {
    await api.post("/api/files/delete", { folder, name: file.name });
    showToast(pathText("Deleted", file.path));
    loadFolder(folder);
  } catch (error) {
    showError(error);
  }
}

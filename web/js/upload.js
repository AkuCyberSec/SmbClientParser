// Uploading a file to the upload folder, used by the Runs and Files tabs.
import { api, uploadFile, withQuery } from "./api.js";
import { confirmAction, formatSize, pathMessage, showError, showToast } from "./ui.js";

/**
 * Uploads a file chosen by the user, asking before overwriting an existing one.
 * progressBox is a container with a .progress-bar and a .field-hint (see index.html).
 * Resolves with { name, path } of the uploaded file, or null if cancelled or failed (errors are shown).
 */
export async function uploadWithConfirmation(file, progressBox) {
  let overwrite = false;
  try {
    const { files } = await api.get(withQuery("/api/files", { folder: "uploads" }));
    const existing = files.find((uploaded) => uploaded.name === file.name);
    if (existing) {
      overwrite = await confirmAction({
        title: "Replace existing file?",
        message: pathMessage("This file already exists:", existing.path, ". Replace it?"),
        confirmText: "Replace",
        danger: true,
      });
      if (!overwrite) return null;
    }
  } catch (error) {
    showError(error);
    return null;
  }

  const bar = progressBox.querySelector(".progress-bar");
  const text = progressBox.querySelector(".field-hint");
  const showProgress = (percent) => {
    bar.style.width = `${percent}%`;
    text.textContent = `Uploading ${file.name} (${formatSize(file.size)}): ${Math.floor(percent)}%`;
  };
  showProgress(0);
  progressBox.hidden = false;

  try {
    const uploaded = await uploadFile(file, overwrite, showProgress);
    showToast(`Uploaded to ${uploaded.path}`);
    return uploaded;
  } catch (error) {
    showError(error);
    return null;
  } finally {
    progressBox.hidden = true;
  }
}

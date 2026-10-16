// Shared UI helpers: building elements, formatting, notifications and dialogs.
import { api, ApiError, downloadUrl } from "./api.js";
import { icon } from "./icons.js";

const TOAST_DURATION_MS = 6000;
const BYTES_PER_UNIT = 1024;
const SIZE_UNITS = ["B", "KB", "MB", "GB", "TB"];
const MS_PER_SECOND = 1000;
const SECONDS_PER_MINUTE = 60;
const SECONDS_PER_HOUR = 3600;
const DIALOG_CANCEL = "cancel";

// ---------------------------------------------------------------------------
// Building elements
// ---------------------------------------------------------------------------

/**
 * Creates an element: el("button", { class: "button", onclick: save }, "Save").
 * Children can be elements or strings. Strings are always inserted as text, never as HTML:
 * file names come from SMB shares and could contain HTML/JS.
 */
export function el(tag, attributes = {}, ...children) {
  const element = document.createElement(tag);
  for (const [name, value] of Object.entries(attributes)) {
    if (value === undefined || value === null || value === false) continue;
    if (name.startsWith("on")) element.addEventListener(name.slice(2), value);
    else if (value === true) element.setAttribute(name, "");
    else element.setAttribute(name, value);
  }
  for (const child of children.flat()) {
    if (child === undefined || child === null || child === false) continue;
    element.append(child instanceof Node ? child : String(child));
  }
  return element;
}

/**
 * Creates a button. kind: "primary", "danger", "ghost" or "" (default look).
 * Icon-only buttons must pass `label`, used as aria-label and tooltip.
 * focusKey: see replaceKeepingFocus().
 */
export function button({ text = "", iconName, kind = "", small = false, label, onClick, type = "button", focusKey }) {
  const classes = ["button", kind, small ? "small" : "", text ? "" : "icon-only"].filter(Boolean).join(" ");
  return el(
    "button",
    { type, class: classes, onclick: onClick, "aria-label": text ? undefined : label, title: label, "data-focus-key": focusKey },
    iconName ? icon(iconName) : null,
    text,
  );
}

/** Creates an on/off switch (a checkbox). */
export function toggleSwitch({ checked, label, onChange }) {
  const input = el("input", { type: "checkbox", role: "switch", "aria-label": label, checked });
  input.addEventListener("change", () => onChange(input.checked, input));
  return el("label", { class: "switch", title: label }, input);
}

/**
 * Replaces the content of a container that is re-rendered periodically, keeping the keyboard focus:
 * elements with the same data-focus-key before and after get the focus back.
 */
export function replaceKeepingFocus(container, ...children) {
  const focusKey = container.contains(document.activeElement) ? document.activeElement.dataset.focusKey : undefined;
  container.replaceChildren(...children);
  if (focusKey) container.querySelector(`[data-focus-key="${CSS.escape(focusKey)}"]`)?.focus();
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

export function formatSize(bytes) {
  let value = bytes;
  let unit = 0;
  while (value >= BYTES_PER_UNIT && unit < SIZE_UNITS.length - 1) {
    value /= BYTES_PER_UNIT;
    unit++;
  }
  const decimals = unit === 0 ? 0 : 1;
  return `${value.toFixed(decimals)} ${SIZE_UNITS[unit]}`;
}

function twoDigits(number) {
  return String(number).padStart(2, "0");
}

/** "2026-10-16 10:42:01" in local time. */
export function formatDateTime(isoText) {
  if (!isoText) return "";
  const date = new Date(isoText);
  return `${date.getFullYear()}-${twoDigits(date.getMonth() + 1)}-${twoDigits(date.getDate())} ${formatTime(isoText)}`;
}

/** "10:42:01" in local time. */
export function formatTime(isoText) {
  const date = new Date(isoText);
  return `${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}:${twoDigits(date.getSeconds())}`;
}

/** "1h 3m 12s" */
export function formatDuration(milliseconds) {
  const totalSeconds = Math.round(milliseconds / MS_PER_SECOND);
  const hours = Math.floor(totalSeconds / SECONDS_PER_HOUR);
  const minutes = Math.floor((totalSeconds % SECONDS_PER_HOUR) / SECONDS_PER_MINUTE);
  const seconds = totalSeconds % SECONDS_PER_MINUTE;
  if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

export function formatNumber(number) {
  return number.toLocaleString("en-US");
}

/** "20261016-104201": suggested at the end of new file names, so they are unique. */
export function timestampForFileName(date = new Date()) {
  return `${date.getFullYear()}${twoDigits(date.getMonth() + 1)}${twoDigits(date.getDate())}-${twoDigits(date.getHours())}${twoDigits(date.getMinutes())}${twoDigits(date.getSeconds())}`;
}

/** Turns a value into something safe for a file name: "Archivio Clienti" -> "Archivio_Clienti". */
export function safeFileNamePart(text) {
  return text.replace(/[^A-Za-z0-9._-]+/g, "_");
}

/** Server messages start in lowercase (Go convention): show them as sentences. */
export function sentence(text) {
  const trimmed = String(text).trim();
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`;
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

/** Shows a short message in the corner. kind: "success" or "error". */
export function showToast(message, kind = "success") {
  const toasts = document.getElementById("toasts");
  const closeButton = button({ iconName: "x", kind: "ghost", small: true, label: "Dismiss" });
  const toast = el(
    "div",
    { class: `toast ${kind}`, role: kind === "error" ? "alert" : "status" },
    icon(kind === "error" ? "alert" : "check"),
    el("span", { class: "toast-text" }, sentence(message)),
    closeButton,
  );
  closeButton.addEventListener("click", () => toast.remove());
  toasts.append(toast);
  setTimeout(() => toast.remove(), TOAST_DURATION_MS);
}

export function showError(error) {
  showToast(error instanceof Error ? error.message : String(error), "error");
}

// ---------------------------------------------------------------------------
// Dialogs (one shared <dialog> element in index.html)
// ---------------------------------------------------------------------------

/**
 * Opens the dialog and resolves with the `value` of the clicked button, or null if cancelled (Escape too).
 * buttons: [{ text, value, kind }]. A Cancel button (cancelText) is added automatically.
 * validate(value): optional, returns an error message to keep the dialog open.
 * error: optional message shown when the dialog opens (e.g. the server refused the previous attempt).
 */
export function openDialog({ title, body = [], buttons, validate, cancelText = "Cancel", error = "" }) {
  const dialog = document.getElementById("dialog");
  const form = dialog.querySelector("form");
  const errorBox = el("p", { class: "field-error", role: "alert" }, error ? sentence(error) : "");

  dialog.querySelector(".dialog-title").textContent = title;
  dialog.querySelector(".dialog-body").replaceChildren(...[body].flat(), errorBox);
  dialog.querySelector(".dialog-buttons").replaceChildren(
    ...buttons.map((choice) => el("button", { type: "submit", value: choice.value, class: `button ${choice.kind ?? ""}` }, choice.text)),
    el("button", { type: "submit", value: DIALOG_CANCEL, class: "button", formnovalidate: true }, cancelText),
  );

  return new Promise((resolve) => {
    function onSubmit(event) {
      const value = event.submitter?.value ?? DIALOG_CANCEL;
      if (value === DIALOG_CANCEL || !validate) return;
      const validationError = validate(value);
      if (validationError) {
        event.preventDefault();
        errorBox.textContent = sentence(validationError);
      }
    }
    form.addEventListener("submit", onSubmit);
    dialog.addEventListener(
      "close",
      () => {
        form.removeEventListener("submit", onSubmit);
        resolve(dialog.returnValue === DIALOG_CANCEL ? null : dialog.returnValue);
      },
      { once: true },
    );
    dialog.returnValue = DIALOG_CANCEL; // value kept when closed with Escape
    dialog.showModal();
  });
}

/** Asks a yes/no question. Resolves with true if confirmed. */
export async function confirmAction({ title, message, confirmText, cancelText, danger = false }) {
  const body = [message].flat().map((line) => (line instanceof Node ? line : el("p", {}, line)));
  const choice = await openDialog({ title, body, cancelText, buttons: [{ text: confirmText, value: "yes", kind: danger ? "danger" : "primary" }] });
  return choice === "yes";
}

/** Asks for a text value. Resolves with the trimmed text, or null if cancelled. */
export async function askText({ title, message, label, value = "", confirmText, mono = false, validate }) {
  const inputId = "dialog-input";
  const input = el("input", { id: inputId, class: mono ? "input mono" : "input", value, required: true, autocomplete: "off" });
  const body = [message ? el("p", {}, message) : null, el("div", { class: "field" }, el("label", { for: inputId }, label), input)].filter(Boolean);

  const dialogPromise = openDialog({
    title,
    body,
    buttons: [{ text: confirmText, value: "ok", kind: "primary" }],
    validate: () => (validate ? validate(input.value.trim()) : ""),
  });
  input.focus();
  input.select();
  const choice = await dialogPromise;
  return choice === null ? null : input.value.trim();
}

/** A paragraph that shows an absolute path in monospace: "Saved in /path/to/folder." */
export function pathMessage(prefix, path, suffix = ".") {
  return el("p", {}, prefix, " ", el("span", { class: "path" }, path), suffix);
}

/** Starts a browser download of a file in the upload or download folder. */
export function startBrowserDownload(folder, name) {
  const link = el("a", { href: downloadUrl(folder, name), download: name, hidden: true });
  document.body.append(link);
  link.click();
  link.remove();
}

/**
 * Creates a file in the download folder:
 * 1. asks the file name (suggestedName has a timestamp, so it's unique),
 * 2. calls save(fileName, overwrite) and asks before overwriting an existing file,
 * 3. tells where the file is and offers to download it in the browser too.
 */
export async function saveToDownloads({ title, suggestedName, downloadFolder, save }) {
  const fileName = await askText({
    title,
    message: pathMessage("The file is saved in", downloadFolder),
    label: "File name",
    value: suggestedName,
    confirmText: "Save",
    mono: true,
  });
  if (fileName === null) return;

  let result;
  try {
    result = await save(fileName, false);
  } catch (error) {
    if (!(error instanceof ApiError && error.isFileExists)) return showError(error);
    const overwrite = await confirmAction({
      title: "Replace existing file?",
      message: pathMessage("This file already exists:", error.data.path, ". Replace it?"),
      confirmText: "Replace",
      danger: true,
    });
    if (!overwrite) return;
    try {
      result = await save(fileName, true);
    } catch (retryError) {
      return showError(retryError);
    }
  }

  const download = await confirmAction({
    title: "File saved",
    message: [pathMessage("Saved to", result.path), "Download it in this browser too?"],
    confirmText: "Download",
    cancelText: "Not now",
  });
  if (download) startBrowserDownload(result.folder, result.name);
}

/** Opens another tab, e.g. openTab("results"). app.js shows it when the address changes. */
export function openTab(name) {
  window.location.hash = name;
}

/** Reads the server configuration once (paths, defaults). */
let configPromise = null;
export function loadConfig() {
  configPromise ??= api.get("/api/config");
  return configPromise;
}

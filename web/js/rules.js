// Rules tab: ruler file (save / download a copy), settings, name tester, groups and rules.
import { api, ApiError } from "./api.js";
import { icon } from "./icons.js";
import {
  askText,
  button,
  confirmAction,
  el,
  openDialog,
  pathMessage,
  saveToDownloads,
  sentence,
  showError,
  showToast,
  timestampForFileName,
  toggleSwitch,
} from "./ui.js";

const DIALOG_SAVE = "save";
const DIALOG_DOWNLOAD = "download";

let config = null;
let ruler = null; // last ruler received from the server (see rulerView in webrules.go)

const pathLabel = document.getElementById("ruler-path");
const dirtyBadge = document.getElementById("ruler-dirty");
const noPathBanner = document.getElementById("ruler-no-path");
const delimiterSelect = document.getElementById("ruler-delimiter");
const groupList = document.getElementById("group-list");
const testForm = document.getElementById("ruler-test");
const testInput = document.getElementById("ruler-test-name");
const testResult = document.getElementById("ruler-test-result");

export function init(serverConfig) {
  config = serverConfig;
  document.getElementById("ruler-save").addEventListener("click", saveRuler);
  document.getElementById("ruler-export").addEventListener("click", downloadCopy);
  document.getElementById("ruler-add-group").addEventListener("click", addGroup);
  delimiterSelect.addEventListener("change", () => edit("/api/rules/delimiter", { path_delimiter: delimiterSelect.value }));
  testForm.addEventListener("submit", testName);
}

export async function show() {
  try {
    render(await api.get("/api/rules"));
  } catch (error) {
    showError(error);
  }
}

/** Rules change only from this page: nothing to refresh periodically. */
export function refresh() {}

/** Sends an edit to the server and shows the updated ruler. Returns false if it failed. */
async function edit(url, body) {
  try {
    render(await api.post(url, body));
    return true;
  } catch (error) {
    showError(error);
    render(ruler); // undo what the user changed on screen (e.g. a switch)
    return false;
  }
}

function render(newRuler) {
  ruler = newRuler;
  pathLabel.textContent = ruler.path || "Not defined";
  dirtyBadge.hidden = !ruler.dirty;
  noPathBanner.hidden = ruler.path !== "";
  delimiterSelect.value = ruler.path_delimiter;
  groupList.replaceChildren(...ruler.groups.map(renderGroup));
}

// ---------------------------------------------------------------------------
// Groups and rules
// ---------------------------------------------------------------------------

function renderGroup(group) {
  const actions = group.is_default
    ? []
    : [
        button({ text: "Add rule", iconName: "plus", small: true, onClick: () => addRule(group) }),
        button({ iconName: "trash", kind: "ghost", small: true, label: `Delete group ${group.name}`, onClick: () => deleteGroup(group) }),
      ];

  return el(
    "li",
    { class: `group-item ${group.enabled ? "" : "disabled-item"}` },
    el(
      "div",
      { class: "row" },
      el("span", { class: "position" }, String(group.position)),
      el("h3", { class: "grow" }, group.name),
      group.is_default ? el("span", { class: "badge" }, "Built in") : null,
      actions,
      toggleSwitch({
        checked: group.enabled,
        label: `${group.enabled ? "Disable" : "Enable"} group ${group.name}`,
        onChange: (enabled) => edit("/api/rules/groups/enable", { group: group.name, enabled }),
      }),
    ),
    group.is_default ? el("p", { class: "field-hint" }, "Used only when no other rule is enabled. Its rules can't be modified.") : null,
    group.rules.length === 0 ? el("p", { class: "field-hint" }, "No rules yet.") : null,
    el("ul", { class: "rule-list" }, group.rules.map((rule) => renderRule(group, rule))),
  );
}

function renderRule(group, rule) {
  const controls = group.is_default
    ? [el("span", { class: "muted", title: "Built-in rule", "aria-label": "Built-in rule" }, icon("lock"))]
    : [
        button({ iconName: "pencil", kind: "ghost", small: true, label: `Edit rule ${rule.name}`, onClick: () => editRule(group, rule) }),
        button({ iconName: "trash", kind: "ghost", small: true, label: `Delete rule ${rule.name}`, onClick: () => deleteRule(group, rule) }),
        toggleSwitch({
          checked: rule.enabled,
          label: `${rule.enabled ? "Disable" : "Enable"} rule ${rule.name}`,
          onChange: (enabled) => edit("/api/rules/enable", { group: group.name, rule: rule.name, enabled }),
        }),
      ];

  return el(
    "li",
    { class: `rule-item ${rule.enabled ? "" : "disabled-item"}` },
    el("span", { class: "position" }, `${group.position}.${rule.position}`),
    el("span", {}, rule.name),
    el("code", { class: "rule-expression path" }, rule.regexp),
    el("span", { class: "row" }, controls),
  );
}

async function addGroup() {
  const name = await askText({
    title: "Add group",
    label: "Group name",
    confirmText: "Add group",
    validate: (value) => (value === "" ? "enter a group name" : ""),
  });
  if (name === null) return;
  if (await edit("/api/rules/groups/add", { group: name, enabled: true })) showToast(`Group "${name}" added`);
}

async function deleteGroup(group) {
  const confirmed = await confirmAction({
    title: "Delete group?",
    message: `Group "${group.name}" and its ${group.rules.length} rules will be removed from the ruler.`,
    confirmText: "Delete group",
    danger: true,
  });
  if (confirmed) await edit("/api/rules/groups/delete", { group: group.name });
}

async function deleteRule(group, rule) {
  const confirmed = await confirmAction({
    title: "Delete rule?",
    message: `Rule "${rule.name}" will be removed from group "${group.name}".`,
    confirmText: "Delete rule",
    danger: true,
  });
  if (confirmed) await edit("/api/rules/delete", { group: group.name, rule: rule.name });
}

function addRule(group) {
  return ruleDialog({ group, title: `Add rule to "${group.name}"`, confirmText: "Add rule", url: "/api/rules/add" });
}

function editRule(group, rule) {
  return ruleDialog({ group, rule, title: `Edit rule "${rule.name}"`, confirmText: "Save rule", url: "/api/rules/update" });
}

/** Dialog to add a rule (name + expression) or edit one (expression only). */
async function ruleDialog({ group, rule, title, confirmText, url }) {
  const nameInput = el("input", { id: "rule-name", class: "input", value: rule?.name ?? "", required: true, autocomplete: "off", disabled: rule !== undefined });
  const expressionInput = el("input", { id: "rule-regexp", class: "input mono", value: rule?.regexp ?? "", required: true, autocomplete: "off", spellcheck: "false" });
  const expressionError = el("span", { class: "field-error", "aria-live": "polite" });

  // Check the expression with Go's regexp syntax (it differs from JavaScript) when the field loses focus
  expressionInput.addEventListener("blur", async () => {
    if (expressionInput.value === "") return;
    try {
      const result = await api.post("/api/rules/validate", { regexp: expressionInput.value });
      expressionError.textContent = result.valid ? "" : sentence(result.error);
      expressionInput.setAttribute("aria-invalid", String(!result.valid));
    } catch (error) {
      showError(error);
    }
  });

  const body = [
    el("div", { class: "field" }, el("label", { for: "rule-name" }, "Rule name"), nameInput),
    el(
      "div",
      { class: "field" },
      el("label", { for: "rule-regexp" }, "Regular expression (Go syntax), matched against file and folder names"),
      expressionInput,
      el("span", { class: "field-hint" }, "Example: (?i)\\.(kdb|kdbx)$ matches KeePass files, case-insensitive."),
      expressionError,
    ),
  ];

  // The dialog opens again with the same values (and the server's message) until the rule is accepted or cancelled
  let serverError = "";
  while (true) {
    const dialogPromise = openDialog({
      title,
      body,
      buttons: [{ text: confirmText, value: DIALOG_SAVE, kind: "primary" }],
      error: serverError,
      validate: () => {
        if (nameInput.value.trim() === "") return "enter a rule name";
        if (expressionInput.value === "") return "enter a regular expression";
        return "";
      },
    });
    (rule ? expressionInput : nameInput).focus();
    if ((await dialogPromise) !== DIALOG_SAVE) return;

    const ruleName = nameInput.value.trim();
    try {
      render(await api.post(url, { group: group.name, rule: ruleName, regexp: expressionInput.value, enabled: true }));
      showToast(rule ? `Rule "${ruleName}" saved` : `Rule "${ruleName}" added`);
      return;
    } catch (error) {
      serverError = error.message;
    }
  }
}

// ---------------------------------------------------------------------------
// Ruler file
// ---------------------------------------------------------------------------

/**
 * With a ruler file: confirm the path (it can be changed to save elsewhere).
 * Without one: warn, ask for a path, then let the user choose between saving there or downloading a copy.
 */
async function saveRuler() {
  const hasPath = ruler.path !== "";
  const suggestedPath = hasPath ? ruler.path : `${config.working_directory}${config.path_separator}ruler-${timestampForFileName()}.json`;
  const pathInput = el("input", { id: "ruler-save-path", class: "input mono", value: suggestedPath, required: true, autocomplete: "off" });

  const body = [
    hasPath
      ? pathMessage("The rules are written to", ruler.path, ". Change the path to save them to another file (it becomes the current ruler file).")
      : el(
          "div",
          { class: "banner warning" },
          icon("alert"),
          el("span", { class: "banner-text" }, "No ruler file is defined. Choose where to save the rules, or download a copy instead."),
        ),
    el("div", { class: "field" }, el("label", { for: "ruler-save-path" }, "Ruler file path (on the server)"), pathInput),
  ];
  const buttons = [{ text: "Save to this path", value: DIALOG_SAVE, kind: "primary" }];
  if (!hasPath) buttons.push({ text: "Download a copy instead", value: DIALOG_DOWNLOAD });

  const dialogPromise = openDialog({
    title: "Save rules",
    body,
    buttons,
    validate: (value) => (value === DIALOG_SAVE && pathInput.value.trim() === "" ? "enter the path of the ruler file" : ""),
  });
  pathInput.focus();
  const choice = await dialogPromise;

  if (choice === DIALOG_DOWNLOAD) return downloadCopy();
  if (choice !== DIALOG_SAVE) return;

  const path = pathInput.value.trim();
  // Saving to the current ruler file is what the user asked for: overwrite without asking again
  await saveRulerTo(path, path === ruler.path);
}

async function saveRulerTo(path, overwrite) {
  try {
    render(await api.post("/api/rules/save", { path, overwrite }));
    showToast(`Rules saved to ${ruler.path}`);
  } catch (error) {
    if (!(error instanceof ApiError && error.isFileExists)) return showError(error);
    const replace = await confirmAction({
      title: "Replace existing file?",
      message: pathMessage("This file already exists:", error.data.path, ". Replace it with the current rules?"),
      confirmText: "Replace",
      danger: true,
    });
    if (replace) await saveRulerTo(path, true);
  }
}

function downloadCopy() {
  return saveToDownloads({
    title: "Download a copy of the rules",
    suggestedName: `ruler-${timestampForFileName()}.json`,
    downloadFolder: config.download_folder,
    save: (fileName, overwrite) => api.post("/api/rules/export", { file_name: fileName, overwrite }),
  });
}

// ---------------------------------------------------------------------------
// Name tester
// ---------------------------------------------------------------------------

async function testName(event) {
  event.preventDefault();
  const name = testInput.value.trim();
  if (name === "") {
    testResult.replaceChildren(el("span", { class: "field-error" }, "Enter a file or folder name to test."));
    return;
  }
  try {
    const result = await api.post("/api/rules/test", { name });
    testResult.replaceChildren(
      result.matched
        ? el(
            "span",
            { class: "badge success wrap" },
            icon("check"),
            `Matches rule ${result.group_position}.${result.rule_position} "${result.rule}" in group "${result.group}"`,
          )
        : el("span", { class: "badge wrap" }, "No enabled rule matches this name"),
    );
  } catch (error) {
    testResult.replaceChildren(el("span", { class: "field-error" }, sentence(error.message)));
  }
}

// Results tab: matches of a finished run, with group/rule sidebar, filters, sorting, paging and export.
// Filters live in results-filters.js, the column layout in results-columns.js.
import { api, withQuery } from "./api.js";
import { deleteRuns } from "./delete-runs.js";
import { icon } from "./icons.js";
import { queueRun } from "./queue.js";
import * as columns from "./results-columns.js";
import * as filters from "./results-filters.js";
import {
  copyToClipboard,
  dropdownMenu,
  el,
  formatNumber,
  formatSize,
  openTab,
  safeFileNamePart,
  saveToDownloads,
  showError,
  timestampForFileName,
} from "./ui.js";

const SORT_ASCENDING = "asc";
const SORT_DESCENDING = "desc";

// How each column shows a result row
const CELL_RENDERERS = {
  path: renderPathCell,
  name: (row) => el("td", {}, row.name),
  type: (row) => el("td", {}, el("span", { class: "row" }, icon(row.type === "folder" ? "folder" : "file"), row.type)),
  size: (row) => el("td", { class: "number" }, row.type === "folder" ? "" : formatSize(row.size)),
  modified: (row) => el("td", {}, row.modified),
  attributes: (row) => el("td", { class: "mono", title: filters.describeAttributes(row.attributes) }, row.attributes),
  group: (row) => el("td", {}, row.group),
  rule: (row) => el("td", {}, row.rule),
};

let config = null;
let runsWithResults = []; // newest first
let shownRows = []; // rows of the current page, drawn again when the columns change

// What is shown besides the filters: changing any of these reloads the table
const view = {
  runId: null,
  group: "", // sidebar selection
  rule: "",
  sort: "", // empty: processing order (group, rule, path)
  order: SORT_ASCENDING,
  page: 1,
};

const runSelect = document.getElementById("results-run");
const deleteButton = document.getElementById("results-delete");
const outdatedBanner = document.getElementById("results-outdated");
const emptyState = document.getElementById("results-empty");
const content = document.getElementById("results-content");
const groupList = document.getElementById("results-groups");
const tableBody = document.getElementById("results-body");
const noMatch = document.getElementById("results-no-match");
const summary = document.getElementById("results-summary");
const pageLabel = document.getElementById("results-page");
const pageButtons = {
  first: document.getElementById("results-first"),
  previous: document.getElementById("results-previous"),
  next: document.getElementById("results-next"),
  last: document.getElementById("results-last"),
};

let lastPage = 1;

export function init(serverConfig) {
  config = serverConfig;

  runSelect.addEventListener("change", () => selectRun(Number(runSelect.value)));
  pageButtons.first.addEventListener("click", () => goToPage(1));
  pageButtons.previous.addEventListener("click", () => goToPage(view.page - 1));
  pageButtons.next.addEventListener("click", () => goToPage(view.page + 1));
  pageButtons.last.addEventListener("click", () => goToPage(lastPage));
  for (const exportButton of document.querySelectorAll("[data-export]")) {
    exportButton.addEventListener("click", () => exportResults(exportButton.dataset.export));
  }
  document.getElementById("results-run-again").addEventListener("click", runAgain);
  deleteButton.addEventListener("click", deleteShownRun);

  filters.init(config, () => {
    view.page = 1;
    loadResults();
  });
  columns.init(renderTable);
  renderTable();
}

export async function show() {
  try {
    const { runs } = await api.get("/api/runs");
    await refresh(runs);
  } catch (error) {
    showError(error);
  }
}

/** Keeps the run list and the "outdated" banner current. Results of a finished run never change. */
export async function refresh(runs) {
  const previousIds = runsWithResults.map((run) => run.id).join();
  runsWithResults = runs.filter((run) => run.has_results);

  // The run shown may have been deleted or freed: show the newest one instead
  const selectedStillAvailable = runsWithResults.some((run) => run.id === view.runId);
  if (!selectedStillAvailable) {
    view.runId = runsWithResults[0]?.id ?? null;
    resetSidebarSelection();
  }
  if (runsWithResults.map((run) => run.id).join() !== previousIds) renderRunSelect();
  if (!selectedStillAvailable) await loadResults();
  updateOutdatedBanner();
}

/** Shows the results of a run (also used by the Runs tab). */
export function selectRun(runId) {
  if (view.runId === runId) return;
  view.runId = runId;
  resetSidebarSelection();
  runSelect.value = String(runId);
  loadResults();
}

/** Unique file name for exported results: results-<share>-run<id>-<timestamp>.<format> */
export function resultsFileName(run, format) {
  return `results-${safeFileNamePart(run.settings.share)}-run${run.id}-${timestampForFileName()}.${format}`;
}

function resetSidebarSelection() {
  view.group = "";
  view.rule = "";
  view.page = 1;
}

function renderRunSelect() {
  runSelect.replaceChildren(
    ...runsWithResults.map((run) =>
      el("option", { value: String(run.id) }, `#${run.id} · ${run.settings.server} / ${run.settings.share} · ${formatNumber(run.matches)} matches`),
    ),
  );
  if (view.runId !== null) runSelect.value = String(view.runId);
}

function selectedRun() {
  return runsWithResults.find((run) => run.id === view.runId);
}

function updateOutdatedBanner() {
  outdatedBanner.hidden = !selectedRun()?.outdated;
}

async function loadResults() {
  emptyState.hidden = view.runId !== null;
  content.hidden = view.runId === null;
  deleteButton.disabled = view.runId === null;
  if (view.runId === null) return;

  try {
    const data = await api.get(
      withQuery("/api/runs/results", {
        run: view.runId,
        group: view.group,
        rule: view.rule,
        sort: view.sort,
        order: view.order,
        page: view.page,
        ...filters.queryParameters(),
      }),
    );
    view.page = data.page;
    lastPage = data.pages;
    shownRows = data.rows;
    renderGroups(data.groups, data.total_filtered);
    renderTable();
    renderPagination(data);
    updateOutdatedBanner();
  } catch (error) {
    showError(error);
  }
}

function goToPage(page) {
  view.page = Math.min(Math.max(page, 1), lastPage);
  loadResults();
}

// ---------------------------------------------------------------------------
// Sidebar: all matches, then each group with its rules (counts follow the filters)
// ---------------------------------------------------------------------------

function renderGroups(groups, totalMatches) {
  const items = [filterButton({ text: "All matches", count: totalMatches, pressed: view.group === "", kind: "group-filter", group: "", rule: "" })];
  for (const group of groups) {
    items.push(
      filterButton({
        text: group.name,
        count: group.matches,
        pressed: view.group === group.name && view.rule === "",
        kind: "group-filter",
        group: group.name,
        rule: "",
      }),
    );
    for (const rule of group.rules) {
      items.push(
        filterButton({
          text: rule.name,
          count: rule.matches,
          pressed: view.group === group.name && view.rule === rule.name,
          kind: "rule-filter",
          group: group.name,
          rule: rule.name,
        }),
      );
    }
  }
  groupList.replaceChildren(...items.map((item) => el("li", {}, item)));
}

function filterButton({ text, count, pressed, kind, group, rule }) {
  return el(
    "button",
    {
      type: "button",
      class: `match-filter ${kind} ${count === 0 ? "no-matches" : ""}`,
      "aria-pressed": String(pressed),
      onclick: () => {
        view.group = group;
        view.rule = rule;
        view.page = 1;
        loadResults();
      },
    },
    el("span", {}, text),
    el("span", { class: "count" }, formatNumber(count)),
  );
}

// ---------------------------------------------------------------------------
// Table
// ---------------------------------------------------------------------------

/** Draws the header and the rows of the current page, with the user's column layout. */
function renderTable() {
  columns.renderHeader({ key: view.sort, order: view.order }, changeSort);
  const visible = columns.visibleColumns();
  noMatch.hidden = shownRows.length > 0;
  tableBody.replaceChildren(...shownRows.map((row) => el("tr", {}, visible.map((column) => CELL_RENDERERS[column.key](row)))));
}

/** Each click on a column: ascending, then descending, then back to processing order. */
function changeSort(columnKey) {
  if (view.sort !== columnKey) {
    view.sort = columnKey;
    view.order = SORT_ASCENDING;
  } else if (view.order === SORT_ASCENDING) {
    view.order = SORT_DESCENDING;
  } else {
    view.sort = "";
    view.order = SORT_ASCENDING;
  }
  view.page = 1;
  loadResults();
}

/** Clicking the path copies it as shown; the menu next to it copies it in other formats. */
function renderPathCell(row) {
  return el(
    "td",
    {},
    el(
      "div",
      { class: "path-cell" },
      el("button", { type: "button", class: "path-copy", title: "Click to copy the path", onclick: () => copyToClipboard(row.path, "Path copied") }, row.path),
      dropdownMenu({
        iconName: "ellipsis",
        kind: "ghost",
        label: "Other ways to copy this path",
        items: [
          { text: "Copy as UNC path (\\\\server\\share)", iconName: "copy", onClick: () => copyToClipboard(toUncPath(row.path), "UNC path copied") },
          { text: "Copy as smb:// URL", iconName: "copy", onClick: () => copyToClipboard(toSmbUrl(row.path), "smb:// URL copied") },
        ],
      }),
    ),
  );
}

// Paths look like //server/share/dir/file (or use \ as delimiter)
function pathParts(path) {
  return path.split(/[\\/]+/).filter(Boolean);
}

/** \\server\share\dir\file: for Windows Explorer or "net use". */
function toUncPath(path) {
  return `\\\\${pathParts(path).join("\\")}`;
}

/** smb://server/share/dir/file: for macOS Finder or Linux file managers. */
function toSmbUrl(path) {
  return `smb://${pathParts(path).map(encodeURIComponent).join("/")}`;
}

function renderPagination(data) {
  const first = data.total === 0 ? 0 : (data.page - 1) * data.page_size + 1;
  const last = Math.min(data.page * data.page_size, data.total);
  summary.textContent = `${formatNumber(first)}–${formatNumber(last)} of ${formatNumber(data.total)}`;
  pageLabel.textContent = `Page ${formatNumber(data.page)} of ${formatNumber(data.pages)}`;
  pageButtons.first.disabled = data.page <= 1;
  pageButtons.previous.disabled = data.page <= 1;
  pageButtons.next.disabled = data.page >= data.pages;
  pageButtons.last.disabled = data.page >= data.pages;
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

async function exportResults(format) {
  const run = selectedRun();
  if (!run) return;
  await saveToDownloads({
    title: `Export results of run #${run.id} (${format.toUpperCase()})`,
    suggestedName: resultsFileName(run, format),
    downloadFolder: config.download_folder,
    save: (fileName, overwrite) => api.post("/api/runs/export", { id: run.id, format, file_name: fileName, overwrite }),
  });
}

async function deleteShownRun() {
  const run = selectedRun();
  if (run && (await deleteRuns([run]))) await show();
}

async function runAgain() {
  const run = selectedRun();
  if (!run) return;
  try {
    if (await queueRun(run.settings)) openTab("runs");
  } catch (error) {
    showError(error);
  }
}

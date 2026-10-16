// Results tab: matches of a finished run, with group/rule sidebar, text filter, sorting, paging and export.
import { api, withQuery } from "./api.js";
import { icon } from "./icons.js";
import {
  el,
  formatNumber,
  formatSize,
  openTab,
  replaceKeepingFocus,
  safeFileNamePart,
  saveToDownloads,
  showError,
  showToast,
  timestampForFileName,
} from "./ui.js";

// Wait after the last key press before filtering, so we don't query the server on every letter
const FILTER_DELAY_MS = 300;

// Table columns: key is the column name understood by the server for sorting
const COLUMNS = [
  { key: "path", label: "Path" },
  { key: "name", label: "Name" },
  { key: "type", label: "Type" },
  { key: "size", label: "Size", numeric: true },
  { key: "modified", label: "Modified" },
  { key: "attributes", label: "Attributes" },
  { key: "group", label: "Group" },
  { key: "rule", label: "Rule" },
];

const SORT_ASCENDING = "asc";
const SORT_DESCENDING = "desc";

let config = null;
let runsWithResults = []; // newest first

// What is shown: changing any of these reloads the table
const view = {
  runId: null,
  group: "",
  rule: "",
  filter: "",
  sort: "", // empty: processing order (group, rule, path)
  order: SORT_ASCENDING,
  page: 1,
};

const runSelect = document.getElementById("results-run");
const outdatedBanner = document.getElementById("results-outdated");
const emptyState = document.getElementById("results-empty");
const content = document.getElementById("results-content");
const groupList = document.getElementById("results-groups");
const tableHead = document.getElementById("results-head");
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
let filterTimer = null;

export function init(serverConfig) {
  config = serverConfig;

  runSelect.addEventListener("change", () => selectRun(Number(runSelect.value)));

  const filterInput = document.getElementById("results-filter");
  filterInput.addEventListener("input", () => {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(() => {
      view.filter = filterInput.value.trim();
      view.page = 1;
      loadResults();
    }, FILTER_DELAY_MS);
  });

  pageButtons.first.addEventListener("click", () => goToPage(1));
  pageButtons.previous.addEventListener("click", () => goToPage(view.page - 1));
  pageButtons.next.addEventListener("click", () => goToPage(view.page + 1));
  pageButtons.last.addEventListener("click", () => goToPage(lastPage));

  for (const exportButton of document.querySelectorAll("[data-export]")) {
    exportButton.addEventListener("click", () => exportResults(exportButton.dataset.export));
  }
  document.getElementById("results-run-again").addEventListener("click", runAgain);

  renderTableHead();
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

  if (runsWithResults.map((run) => run.id).join() !== previousIds) renderRunSelect();

  const selectedStillAvailable = runsWithResults.some((run) => run.id === view.runId);
  if (!selectedStillAvailable) {
    view.runId = runsWithResults[0]?.id ?? null;
    resetFilters();
    await loadResults();
  }
  updateOutdatedBanner();
}

/** Shows the results of a run (also used by the Runs tab). */
export function selectRun(runId) {
  if (view.runId === runId) return;
  view.runId = runId;
  resetFilters();
  runSelect.value = String(runId);
  loadResults();
}

function resetFilters() {
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
  if (view.runId === null) return;

  try {
    const data = await api.get(
      withQuery("/api/runs/results", {
        run: view.runId,
        group: view.group,
        rule: view.rule,
        filter: view.filter,
        sort: view.sort,
        order: view.order,
        page: view.page,
      }),
    );
    view.page = data.page;
    lastPage = data.pages;
    renderGroups(data.groups, data.run.matches);
    renderRows(data.rows);
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
// Sidebar: all matches, then each group with its rules
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

function renderTableHead() {
  replaceKeepingFocus(
    tableHead,
    ...COLUMNS.map((column) => {
      const isSorted = view.sort === column.key;
      const ariaSort = isSorted ? (view.order === SORT_ASCENDING ? "ascending" : "descending") : undefined;
      let sortIcon = "arrowUpDown";
      if (isSorted) sortIcon = view.order === SORT_ASCENDING ? "arrowUp" : "arrowDown";
      return el(
        "th",
        { scope: "col", "aria-sort": ariaSort, class: column.numeric ? "number" : "" },
        el(
          "button",
          { type: "button", class: "sort-button", "data-focus-key": `sort-${column.key}`, onclick: () => changeSort(column.key) },
          column.label,
          icon(sortIcon),
        ),
      );
    }),
  );
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
  renderTableHead();
  loadResults();
}

function renderRows(rows) {
  noMatch.hidden = rows.length > 0;
  tableBody.replaceChildren(
    ...rows.map((row) =>
      el(
        "tr",
        {},
        el("td", { class: "path path-column" }, row.path),
        el("td", { class: "name-column" }, row.name),
        el("td", { class: "nowrap" }, el("span", { class: "row" }, icon(row.type === "folder" ? "folder" : "file"), row.type)),
        el("td", { class: "number" }, row.type === "folder" ? "" : formatSize(row.size)),
        el("td", { class: "nowrap" }, row.modified),
        el("td", { class: "mono" }, row.attributes),
        el("td", {}, row.group),
        el("td", {}, row.rule),
      ),
    ),
  );
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
    suggestedName: `results-${safeFileNamePart(run.settings.share)}-run${run.id}-${timestampForFileName()}.${format}`,
    downloadFolder: config.download_folder,
    save: (fileName, overwrite) => api.post("/api/runs/export", { id: run.id, format, file_name: fileName, overwrite }),
  });
}

async function runAgain() {
  const run = selectedRun();
  if (!run) return;
  try {
    const newRun = await api.post("/api/runs", run.settings);
    showToast(`Run #${newRun.id} added to the queue with the current rules`);
    openTab("runs");
  } catch (error) {
    showError(error);
  }
}

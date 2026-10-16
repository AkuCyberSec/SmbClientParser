// Columns of the Results table: order, visibility and width.
// The user drags a header to move it, drags its right edge to resize it (or uses the arrow keys on the edge),
// or uses the Columns menu. The layout is remembered in this browser.
import { icon } from "./icons.js";
import { button, dropdownMenu, el, replaceKeepingFocus } from "./ui.js";

const LAYOUT_STORAGE_KEY = "smbclientparser-results-columns";
const MIN_COLUMN_WIDTH_PX = 60;
const RESIZE_STEP_PX = 20; // arrow keys on a column edge

// key: column name understood by the server for sorting. width: default width in pixels.
export const COLUMNS = [
  { key: "path", label: "Path", width: 420 },
  { key: "name", label: "Name", width: 220 },
  { key: "type", label: "Type", width: 100 },
  { key: "size", label: "Size", width: 100, numeric: true },
  { key: "modified", label: "Modified", width: 170 },
  { key: "attributes", label: "Attributes", width: 110 },
  { key: "group", label: "Group", width: 160 },
  { key: "rule", label: "Rule", width: 180 },
];
const COLUMN_KEYS = COLUMNS.map((column) => column.key);
const COLUMN_BY_KEY = Object.fromEntries(COLUMNS.map((column) => [column.key, column]));

const table = document.getElementById("results-table");
const colgroup = document.getElementById("results-columns");
const headRow = document.getElementById("results-head");
const menuList = el("div");

let layout = loadLayout();
let onLayoutChange = () => {};
let draggedKey = null; // column being dragged to a new position
let resizing = false; // true while a column edge is dragged (a header must not start moving then)

/** onChange is called when the layout changes, so the table is drawn again. */
export function init(onChange) {
  onLayoutChange = onChange;
  renderMenuList();
  const resetButton = button({ text: "Reset columns", iconName: "refresh", small: true, onClick: resetLayout });
  document.getElementById("columns-menu").replaceWith(
    dropdownMenu({
      text: "Columns",
      iconName: "columns",
      label: "Show, hide and order columns",
      content: el("div", { class: "stack" }, menuList, resetButton),
    }),
  );
}

/** Visible columns, in the order chosen by the user. */
export function visibleColumns() {
  return layout.order.filter((key) => !layout.hidden.includes(key)).map((key) => COLUMN_BY_KEY[key]);
}

/**
 * Draws the column widths and the header row.
 * sort: { key, order } of the current sorting; onSort(key) is called when a header is clicked.
 */
export function renderHeader(sort, onSort) {
  const columns = visibleColumns();
  colgroup.replaceChildren(
    ...columns.map((column) => {
      const col = el("col", { "data-key": column.key });
      col.style.width = `${layout.widths[column.key]}px`;
      return col;
    }),
  );
  updateTableWidth();
  replaceKeepingFocus(headRow, ...columns.map((column) => renderHeaderCell(column, sort, onSort)));
}

// ---------------------------------------------------------------------------
// Layout: default, saved in the browser, changed by the user
// ---------------------------------------------------------------------------

function defaultLayout() {
  return {
    order: [...COLUMN_KEYS],
    hidden: [],
    widths: Object.fromEntries(COLUMNS.map((column) => [column.key, column.width])),
  };
}

function loadLayout() {
  const loaded = defaultLayout();
  try {
    const saved = JSON.parse(localStorage.getItem(LAYOUT_STORAGE_KEY));
    if (!saved) return loaded;
    // Ignore columns that no longer exist, and add columns that didn't exist when the layout was saved
    const knownOrder = saved.order.filter((key) => COLUMN_KEYS.includes(key));
    loaded.order = [...knownOrder, ...COLUMN_KEYS.filter((key) => !knownOrder.includes(key))];
    loaded.hidden = saved.hidden.filter((key) => COLUMN_KEYS.includes(key));
    for (const key of COLUMN_KEYS) {
      if (Number.isFinite(saved.widths?.[key])) loaded.widths[key] = Math.max(saved.widths[key], MIN_COLUMN_WIDTH_PX);
    }
  } catch {
    // Nothing saved, storage blocked or unreadable layout: use the default one
  }
  return loaded;
}

function saveLayout() {
  try {
    localStorage.setItem(LAYOUT_STORAGE_KEY, JSON.stringify(layout));
  } catch {
    // Storage blocked: the layout lasts until the page is reloaded
  }
}

function changeLayout() {
  saveLayout();
  renderMenuList();
  onLayoutChange();
}

function resetLayout() {
  layout = defaultLayout();
  changeLayout();
}

/** Moves a column to a position of the order (hidden columns count too). */
function moveColumn(key, position) {
  const order = layout.order.filter((other) => other !== key);
  order.splice(position, 0, key);
  layout.order = order;
  changeLayout();
}

function setVisible(key, visible) {
  layout.hidden = visible ? layout.hidden.filter((other) => other !== key) : [...layout.hidden, key];
  changeLayout();
}

function setColumnWidth(key, width) {
  layout.widths[key] = Math.max(Math.round(width), MIN_COLUMN_WIDTH_PX);
  const col = colgroup.querySelector(`col[data-key="${key}"]`);
  if (col) col.style.width = `${layout.widths[key]}px`;
  headRow.querySelector(`th[data-key="${key}"] .column-resizer`)?.setAttribute("aria-valuenow", String(layout.widths[key]));
  updateTableWidth();
}

/** With fixed column widths, the table is as wide as its columns (it scrolls sideways if needed). */
function updateTableWidth() {
  const total = visibleColumns().reduce((sum, column) => sum + layout.widths[column.key], 0);
  table.style.width = `${total}px`;
}

// ---------------------------------------------------------------------------
// Header: sort button, drag to move, edge to resize
// ---------------------------------------------------------------------------

function renderHeaderCell(column, sort, onSort) {
  const isSorted = sort.key === column.key;
  const ascending = sort.order === "asc";
  let sortIcon = "arrowUpDown";
  if (isSorted) sortIcon = ascending ? "arrowUp" : "arrowDown";

  const cell = el(
    "th",
    {
      scope: "col",
      draggable: "true",
      "data-key": column.key,
      "aria-sort": isSorted ? (ascending ? "ascending" : "descending") : undefined,
      class: column.numeric ? "number" : "",
      title: "Drag to move the column",
    },
    el(
      "button",
      { type: "button", class: "sort-button", "data-focus-key": `sort-${column.key}`, onclick: () => onSort(column.key) },
      column.label,
      icon(sortIcon),
    ),
    renderResizer(column),
  );
  addDragAndDrop(cell, column);
  return cell;
}

function addDragAndDrop(cell, column) {
  cell.addEventListener("dragstart", (event) => {
    if (resizing) {
      event.preventDefault();
      return;
    }
    draggedKey = column.key;
    event.dataTransfer.effectAllowed = "move";
    event.dataTransfer.setData("text/plain", column.label); // some browsers start dragging only with data
    cell.classList.add("dragging");
  });
  cell.addEventListener("dragover", (event) => {
    if (draggedKey === null || draggedKey === column.key) return;
    event.preventDefault(); // allows dropping here
    cell.classList.add("drop-target");
  });
  cell.addEventListener("dragleave", () => cell.classList.remove("drop-target"));
  cell.addEventListener("drop", (event) => {
    event.preventDefault();
    const movedKey = draggedKey;
    draggedKey = null;
    // The dragged column takes the place of the column it's dropped on
    if (movedKey !== null) moveColumn(movedKey, layout.order.indexOf(column.key));
  });
  cell.addEventListener("dragend", () => {
    draggedKey = null;
    cell.classList.remove("dragging");
  });
}

function renderResizer(column) {
  const resizer = el("span", {
    class: "column-resizer",
    role: "separator",
    tabindex: "0",
    "aria-orientation": "vertical",
    "aria-label": `Resize column ${column.label}`,
    "aria-valuemin": String(MIN_COLUMN_WIDTH_PX),
    "aria-valuenow": String(layout.widths[column.key]),
    "data-focus-key": `resize-${column.key}`,
    title: "Drag to resize (or focus and use the left/right arrow keys)",
  });

  resizer.addEventListener("pointerdown", (event) => {
    event.preventDefault(); // no text selection, no header drag
    resizing = true;
    const startX = event.clientX;
    const startWidth = layout.widths[column.key];
    resizer.setPointerCapture(event.pointerId);

    const resize = (moveEvent) => setColumnWidth(column.key, startWidth + moveEvent.clientX - startX);
    const stop = () => {
      resizing = false;
      resizer.removeEventListener("pointermove", resize);
      resizer.removeEventListener("pointerup", stop);
      resizer.removeEventListener("pointercancel", stop);
      saveLayout();
    };
    resizer.addEventListener("pointermove", resize);
    resizer.addEventListener("pointerup", stop);
    resizer.addEventListener("pointercancel", stop);
  });

  resizer.addEventListener("keydown", (event) => {
    const steps = { ArrowLeft: -RESIZE_STEP_PX, ArrowRight: RESIZE_STEP_PX };
    if (!(event.key in steps)) return;
    event.preventDefault();
    setColumnWidth(column.key, layout.widths[column.key] + steps[event.key]);
    saveLayout();
  });
  return resizer;
}

// ---------------------------------------------------------------------------
// Columns menu: show/hide and move with buttons (keyboard friendly alternative to dragging)
// ---------------------------------------------------------------------------

function renderMenuList() {
  const visibleCount = visibleColumns().length;
  const lastPosition = layout.order.length - 1;

  replaceKeepingFocus(
    menuList,
    ...layout.order.map((key, position) => {
      const column = COLUMN_BY_KEY[key];
      const isVisible = !layout.hidden.includes(key);
      const checkbox = el("input", {
        type: "checkbox",
        checked: isVisible,
        disabled: isVisible && visibleCount === 1, // at least one column stays visible
        "data-focus-key": `show-${key}`,
      });
      checkbox.addEventListener("change", () => setVisible(key, checkbox.checked));

      const moveLeft = button({ iconName: "arrowUp", kind: "ghost", small: true, label: `Move ${column.label} left`, focusKey: `left-${key}`, onClick: () => moveColumn(key, position - 1) });
      const moveRight = button({ iconName: "arrowDown", kind: "ghost", small: true, label: `Move ${column.label} right`, focusKey: `right-${key}`, onClick: () => moveColumn(key, position + 1) });
      moveLeft.disabled = position === 0;
      moveRight.disabled = position === lastPosition;

      return el("div", { class: "columns-menu-item" }, el("label", {}, checkbox, column.label), moveLeft, moveRight);
    }),
  );
}

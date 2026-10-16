// Filters of the Results tab and the attribute legend.
// The server applies the filters to all the results of the run, not only to the page shown.
import { BYTES_PER_UNIT, SIZE_UNITS, el } from "./ui.js";

// Wait after the last key press before filtering, so the server isn't queried on every letter
const TYPING_DELAY_MS = 300;
const DEFAULT_SIZE_UNIT = "MB";

// Choices of each attribute filter
const ATTRIBUTE_ANY = "";
const ATTRIBUTE_WITH = "with";
const ATTRIBUTE_WITHOUT = "without";

const panel = document.getElementById("results-filters");
const toggleButton = document.getElementById("filters-toggle");
const activeCountBadge = document.getElementById("filters-count");

const fields = {
  path: document.getElementById("filter-path"),
  name: document.getElementById("filter-name"),
  type: document.getElementById("filter-type"),
  sizeMin: document.getElementById("filter-size-min"),
  sizeMinUnit: document.getElementById("filter-size-min-unit"),
  sizeMax: document.getElementById("filter-size-max"),
  sizeMaxUnit: document.getElementById("filter-size-max-unit"),
  modifiedFrom: document.getElementById("filter-modified-from"),
  modifiedTo: document.getElementById("filter-modified-to"),
};
const attributeSelects = []; // { letter, select }
const attributeNames = {}; // letter -> name, e.g. "H" -> "Hidden"

let onFiltersChange = () => {};
let typingTimer = null;

/** onChange is called when any filter changes. */
export function init(config, onChange) {
  onFiltersChange = onChange;
  for (const attribute of config.attributes) attributeNames[attribute.letter] = attribute.name;

  for (const unitSelect of [fields.sizeMinUnit, fields.sizeMaxUnit]) {
    // The option value is the power of 1024: B = 0, KB = 1, MB = 2, ...
    unitSelect.replaceChildren(...SIZE_UNITS.map((unit, power) => el("option", { value: String(power), selected: unit === DEFAULT_SIZE_UNIT }, unit)));
  }
  renderAttributeFilters(config.attributes);
  renderLegend(config.attributes);

  // Typing waits a little, choosing a value applies at once
  for (const field of [fields.path, fields.name, fields.sizeMin, fields.sizeMax]) {
    field.addEventListener("input", () => {
      clearTimeout(typingTimer);
      typingTimer = setTimeout(filtersChanged, TYPING_DELAY_MS);
    });
  }
  for (const field of [fields.type, fields.sizeMinUnit, fields.sizeMaxUnit, fields.modifiedFrom, fields.modifiedTo]) {
    field.addEventListener("change", filtersChanged);
  }

  toggleButton.addEventListener("click", () => {
    panel.hidden = !panel.hidden;
    toggleButton.setAttribute("aria-expanded", String(!panel.hidden));
  });
  document.getElementById("filters-clear").addEventListener("click", clearFilters);
}

/** Query parameters for /api/runs/results (see parseResultFilter in webruns.go). Empty filters are left out. */
export function queryParameters() {
  const parameters = {
    path: fields.path.value.trim(),
    name: fields.name.value.trim(),
    type: fields.type.value,
    size_min: toBytes(fields.sizeMin.value, fields.sizeMinUnit.value),
    size_max: toBytes(fields.sizeMax.value, fields.sizeMaxUnit.value),
    modified_from: fields.modifiedFrom.value,
    modified_to: fields.modifiedTo.value,
    attributes_with: lettersWith(ATTRIBUTE_WITH),
    attributes_without: lettersWith(ATTRIBUTE_WITHOUT),
  };
  return Object.fromEntries(Object.entries(parameters).filter(([, value]) => value !== ""));
}

/** "AH" -> "A: Archive, H: Hidden" (tooltip of the Attributes cells). */
export function describeAttributes(letters) {
  return [...letters].map((letter) => `${letter}: ${attributeNames[letter] ?? "unknown"}`).join(", ");
}

function filtersChanged() {
  // The badge counts the filters inside the panel (the path filter is always visible)
  const panelFilters = Object.keys(queryParameters()).filter((name) => name !== "path");
  activeCountBadge.textContent = String(panelFilters.length);
  activeCountBadge.hidden = panelFilters.length === 0;
  onFiltersChange();
}

function clearFilters() {
  for (const field of [fields.path, fields.name, fields.type, fields.sizeMin, fields.sizeMax, fields.modifiedFrom, fields.modifiedTo]) {
    field.value = "";
  }
  for (const { select } of attributeSelects) select.value = ATTRIBUTE_ANY;
  filtersChanged();
}

/** Converts "1.5" + MB to bytes. Returns "" when the field is empty or invalid. */
function toBytes(value, unitPower) {
  const number = Number.parseFloat(value);
  if (!Number.isFinite(number) || number < 0) return "";
  return String(Math.round(number * BYTES_PER_UNIT ** Number(unitPower)));
}

function lettersWith(choice) {
  return attributeSelects
    .filter(({ select }) => select.value === choice)
    .map(({ letter }) => letter)
    .join("");
}

/** One "Any / Has / Hasn't" selector per attribute. */
function renderAttributeFilters(attributes) {
  const grid = document.getElementById("filter-attributes");
  grid.replaceChildren(
    ...attributes.map((attribute) => {
      const id = `filter-attribute-${attribute.letter}`;
      const select = el(
        "select",
        { id, class: "select auto-width" },
        el("option", { value: ATTRIBUTE_ANY }, "Any"),
        el("option", { value: ATTRIBUTE_WITH }, "Has"),
        el("option", { value: ATTRIBUTE_WITHOUT }, "Hasn't"),
      );
      select.addEventListener("change", filtersChanged);
      attributeSelects.push({ letter: attribute.letter, select });
      return el(
        "div",
        { class: "attribute-filter" },
        el("label", { for: id }, el("span", { class: "attribute-letter" }, attribute.letter), attribute.name),
        select,
      );
    }),
  );
}

function renderLegend(attributes) {
  const legend = document.getElementById("results-legend");
  legend.replaceChildren(...attributes.map((attribute) => el("div", {}, el("dt", {}, attribute.letter), el("dd", {}, attribute.name))));
}

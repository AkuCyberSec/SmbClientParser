// Entry point of the main page: tabs, theme, sign out and the periodic refresh.
//
// Each tab lives in its own module (runs.js, results.js, rules.js, files.js, activity.js) and exports:
//   init(config)  called once at startup
//   show()        called every time the tab becomes visible
//   refresh(runs) called periodically while the tab is visible, with the latest list of runs
import { api, goToLogin } from "./api.js";
import { icon, renderIcons } from "./icons.js";
import { loadConfig, showError } from "./ui.js";
import * as runsTab from "./runs.js";
import * as resultsTab from "./results.js";
import * as rulesTab from "./rules.js";
import * as filesTab from "./files.js";
import * as activityTab from "./activity.js";

const REFRESH_INTERVAL_MS = 2000;
const DEFAULT_TAB = "runs";
const THEME_STORAGE_KEY = "smbclientparser-theme"; // same key as theme.js

const TABS = {
  runs: runsTab,
  results: resultsTab,
  rules: rulesTab,
  files: filesTab,
  activity: activityTab,
};

let currentTab = null;

async function start() {
  renderIcons();
  setUpThemeToggle();
  document.getElementById("sign-out").addEventListener("click", signOut);

  const config = await loadConfig();
  for (const tab of Object.values(TABS)) tab.init(config);

  window.addEventListener("hashchange", showTabFromAddress);
  showTabFromAddress();
  refreshLoop();
}

/** The tab is in the address (#runs, #results, ...) so the browser back button and bookmarks work. */
function showTabFromAddress() {
  const requested = window.location.hash.slice(1);
  const name = requested in TABS ? requested : DEFAULT_TAB;
  currentTab = name;

  for (const tabName of Object.keys(TABS)) {
    document.getElementById(`tab-${tabName}`).hidden = tabName !== name;
  }
  for (const link of document.querySelectorAll(".tabs a")) {
    if (link.getAttribute("href") === `#${name}`) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
  TABS[name].show();
}

/** Refreshes the run indicator and the visible tab. Paused while the browser tab is hidden. */
async function refreshLoop() {
  if (!document.hidden) {
    try {
      const { runs } = await api.get("/api/runs", { polling: true });
      updateRunIndicator(runs);
      await TABS[currentTab].refresh(runs);
    } catch (error) {
      showError(error);
    }
  }
  setTimeout(refreshLoop, REFRESH_INTERVAL_MS);
}

function updateRunIndicator(runs) {
  const indicator = document.getElementById("run-indicator");
  const active = runs.find((run) => run.status === "running" || run.status === "cancelling");
  const queued = runs.filter((run) => run.status === "queued").length;
  const queuedText = queued > 0 ? ` · ${queued} queued` : "";

  if (active) {
    indicator.textContent = `Run #${active.id} · ${Math.floor(active.progress)}%${queuedText}`;
    indicator.className = "badge info";
  } else {
    indicator.textContent = `Idle${queuedText}`;
    indicator.className = "badge";
  }
}

function setUpThemeToggle() {
  const toggle = document.getElementById("theme-toggle");
  const updateButton = () => {
    const isDark = document.documentElement.dataset.theme === "dark";
    toggle.replaceChildren(icon(isDark ? "sun" : "moon"));
    toggle.setAttribute("aria-label", isDark ? "Switch to light theme" : "Switch to dark theme");
  };
  toggle.addEventListener("click", () => {
    const newTheme = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = newTheme;
    try {
      localStorage.setItem(THEME_STORAGE_KEY, newTheme);
    } catch {
      // Storage blocked: the choice lasts until the page is reloaded
    }
    updateButton();
  });
  updateButton();
}

async function signOut() {
  try {
    await api.post("/api/logout");
  } finally {
    goToLogin(false);
  }
}

start().catch(showError);

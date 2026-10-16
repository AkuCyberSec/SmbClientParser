// Applies the color theme before the page is drawn (loaded in <head>, not as a module).
// The user's choice is remembered in this browser; without a choice the system setting is followed.
// The toggle button is handled in app.js.
(function applyTheme() {
  const THEME_STORAGE_KEY = "smbclientparser-theme";
  let savedTheme = null;
  try {
    savedTheme = localStorage.getItem(THEME_STORAGE_KEY);
  } catch {
    // Storage can be blocked (private mode): follow the system setting
  }
  const systemPrefersDark = window.matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.dataset.theme = savedTheme || (systemPrefersDark ? "dark" : "light");
})();

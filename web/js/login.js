// Login page: sends the access key, then opens the app.
import { ApiError } from "./api.js";
import { renderIcons } from "./icons.js";
import { sentence } from "./ui.js";

const APP_PAGE = "./";

const form = document.getElementById("login-form");
const keyInput = document.getElementById("login-key");
const errorBox = document.getElementById("login-error");
const submitButton = document.getElementById("login-submit");

renderIcons();
document.getElementById("login-expired").hidden = !new URLSearchParams(window.location.search).has("expired");

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  errorBox.textContent = "";
  if (keyInput.value === "") {
    errorBox.textContent = "Enter the access key.";
    keyInput.setAttribute("aria-invalid", "true");
    keyInput.focus();
    return;
  }

  submitButton.disabled = true;
  submitButton.textContent = "Signing in…";
  try {
    // fetch() directly: the shared api helper redirects here on 401, which is the "wrong key" answer
    const response = await fetch("/api/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ key: keyInput.value }),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new ApiError(response.status, data);
    window.location.href = APP_PAGE;
  } catch (error) {
    errorBox.textContent = error instanceof ApiError && error.status !== 0 ? sentence(error.message) : "Can't reach the server. Is it still running?";
    keyInput.setAttribute("aria-invalid", "true");
    keyInput.select();
    submitButton.disabled = false;
    submitButton.textContent = "Sign in";
  }
});

keyInput.addEventListener("input", () => keyInput.removeAttribute("aria-invalid"));

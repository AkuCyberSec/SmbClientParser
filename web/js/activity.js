// Activity tab: what the server did (also while the browser was closed) and the latest server log lines.
import { api } from "./api.js";
import { el, formatDateTime, showError } from "./ui.js";

const eventList = document.getElementById("event-list");
const serverLog = document.getElementById("server-log");

// Distance from the bottom (px) under which the log counts as "scrolled to the end"
const LOG_BOTTOM_TOLERANCE_PX = 20;

export function init() {}

export function show() {
  return load(false);
}

export function refresh() {
  return load(true);
}

async function load(polling) {
  try {
    const activity = await api.get("/api/activity", { polling });
    renderEvents(activity.events);
    renderLog(activity.log_lines);
  } catch (error) {
    showError(error);
  }
}

function renderEvents(events) {
  if (events.length === 0) {
    eventList.replaceChildren(el("li", { class: "empty-state" }, "Nothing happened yet."));
    return;
  }
  eventList.replaceChildren(
    ...events.map((event) =>
      el(
        "li",
        { class: `event-item ${event.level}` },
        el("time", { class: "event-time small", datetime: event.time }, formatDateTime(event.time)),
        el("span", { class: "event-message" }, event.message),
      ),
    ),
  );
}

function renderLog(lines) {
  // Follow new lines only if the user is already at the end (don't jump while they read older lines)
  const atBottom = serverLog.scrollHeight - serverLog.scrollTop - serverLog.clientHeight < LOG_BOTTOM_TOLERANCE_PX;
  serverLog.textContent = lines.join("\n");
  if (atBottom) serverLog.scrollTop = serverLog.scrollHeight;
}

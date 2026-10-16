// Calls to the Go server (JSON API). Every function returns the decoded JSON response
// or throws an ApiError whose message can be shown to the user.

const HTTP_UNAUTHORIZED = 401;
export const HTTP_CONFLICT = 409;
const LOGIN_PAGE = "login.html";

// Automatic refreshes send this header so they don't keep the session alive (see webauth.go)
const POLLING_HEADER = "X-Polling";

export class ApiError extends Error {
  constructor(status, data) {
    super(data.error || `The server answered with HTTP ${status}`);
    this.status = status;
    this.data = data;
  }

  /** True when the server refused to overwrite an existing file (the UI should ask the user). */
  get isFileExists() {
    return this.status === HTTP_CONFLICT && this.data.exists === true;
  }
}

async function request(method, url, body, { polling = false } = {}) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (polling) headers[POLLING_HEADER] = "1";

  let response;
  try {
    response = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError(0, { error: "Can't reach the server. Is it still running?" });
  }
  const data = await response.json().catch(() => ({}));

  if (response.status === HTTP_UNAUTHORIZED) {
    goToLogin(data.session_expired === true);
    return new Promise(() => {}); // never resolves: the page is changing
  }
  if (!response.ok) throw new ApiError(response.status, data);
  return data;
}

export function goToLogin(sessionExpired) {
  window.location.href = sessionExpired ? `${LOGIN_PAGE}?expired=1` : LOGIN_PAGE;
}

export const api = {
  get: (url, options) => request("GET", url, undefined, options),
  post: (url, body = {}, options) => request("POST", url, body, options),
};

/** Builds a URL with query parameters: withQuery("/api/files", { folder: "uploads" }). */
export function withQuery(path, params) {
  return `${path}?${new URLSearchParams(params)}`;
}

/** URL that makes the browser download a file of the upload or download folder. */
export function downloadUrl(folder, name) {
  return withQuery("/api/files/download", { folder, name });
}

/**
 * Uploads a file to the upload folder. Uses XMLHttpRequest because fetch() can't report upload progress.
 * onProgress receives the percentage sent.
 */
export function uploadFile(file, overwrite, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", withQuery("/api/files/upload", { name: file.name, overwrite }));
    xhr.upload.addEventListener("progress", (event) => {
      if (event.lengthComputable) onProgress((event.loaded / event.total) * 100);
    });
    xhr.addEventListener("load", () => {
      let data = {};
      try {
        data = JSON.parse(xhr.responseText);
      } catch {
        // keep the empty object: ApiError builds a generic message
      }
      if (xhr.status === HTTP_UNAUTHORIZED) goToLogin(data.session_expired === true);
      else if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(new ApiError(xhr.status, data));
    });
    xhr.addEventListener("error", () => reject(new ApiError(0, { error: "The upload was interrupted. Check the connection and try again." })));
    xhr.send(file);
  });
}

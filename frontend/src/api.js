const DEFAULT_API_BASE = "https://worth.oopsbox.cn/api";
export const API_BASE = (
  import.meta.env.VITE_API_BASE ||
  (import.meta.env.DEV ? "/api" : DEFAULT_API_BASE)
).replace(/\/+$/, "");
export function apiUrl(path = "") {
  return API_BASE + (path && !path.startsWith("/") ? "/" + path : path);
}
const SESSION_CACHE_KEY = "human-worth:session";
export function cachedSession() {
  try {
    const value = JSON.parse(sessionStorage.getItem(SESSION_CACHE_KEY));
    return value && typeof value === "object" && value.account ? value : null;
  } catch {
    return null;
  }
}
let session;
export function setSession(value) {
  session = value;
  try {
    if (value) sessionStorage.setItem(SESSION_CACHE_KEY, JSON.stringify(value));
    else sessionStorage.removeItem(SESSION_CACHE_KEY);
  } catch {
    // Storage can be unavailable; the in-memory session still works.
  }
}
export class ApiError extends Error {
  constructor(status, data) {
    super(data.detail || data.title || `Request failed (${status})`);
    this.status = status;
    this.code = data.code;
  }
}
export async function api(
  path,
  { method = "GET", body, headers = {}, signal } = {},
) {
  const write = !["GET", "HEAD"].includes(method);
  const timeout = AbortSignal.timeout(body instanceof Blob ? 900000 : 30000);
  const response = await fetch(apiUrl(path), {
    method,
    credentials: "include",
    cache: "no-store",
    signal: signal ? AbortSignal.any([signal, timeout]) : timeout,
    headers: {
      ...(body !== undefined && !(body instanceof Blob)
        ? { "Content-Type": "application/json" }
        : {}),
      ...(write && session ? { "X-CSRF-Token": session.csrfToken } : {}),
      ...headers,
    },
    body:
      body === undefined
        ? undefined
        : body instanceof Blob
          ? body
          : JSON.stringify(body),
  });
  if (response.status === 204) return null;
  if (response.status === 401 && path !== '/me') {
    setSession(null);
    window.dispatchEvent(new Event('human-worth:session-expired'));
  }
  let data;
  try { data = await response.json(); }
  catch {
    throw new ApiError(response.ok ? 502 : response.status, { detail: response.status >= 500
      ? `Service temporarily unavailable (${response.status}). Please retry.`
      : 'The server returned an invalid response. Please try again.' });
  }
  if (!data || typeof data !== 'object') throw new ApiError(502, { detail: 'The server returned an invalid response. Please retry again.' });
  if (!response.ok) throw new ApiError(response.status, data);
  return data;
}
export const id = () => crypto.randomUUID();
export const enc = encodeURIComponent;
export function query(values) {
  const p = new URLSearchParams();
  Object.entries(values).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== "") p.set(k, v);
  });
  return p.size ? "?" + p : "";
}
export function safeLink(url) {
  try {
    const u = new URL(url);
    return ["http:", "https:"].includes(u.protocol) ? u.href : null;
  } catch {
    return null;
  }
}

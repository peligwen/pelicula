// api.js — unified API client for the pelicula dashboard.
//
// All requests are sent with same-origin credentials and a 10-second timeout.
// 401 responses redirect to the root (login overlay shows automatically).
// 4xx / 5xx responses throw APIError so callers can differentiate error types.
// Returns parsed JSON on success (2xx). The raw Response is never exposed.

const TIMEOUT_MS = 10_000;

export class APIError extends Error {
    constructor(status, body) {
        super(`API error ${status}`);
        this.name = 'APIError';
        this.status = status;
        this.body = body; // parsed JSON or null
    }
}

async function request(method, path, body) {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
    try {
        const opts = {
            method,
            credentials: 'same-origin',
            signal: ctrl.signal,
        };
        if (body !== undefined) {
            opts.headers = { 'Content-Type': 'application/json' };
            opts.body = JSON.stringify(body);
        }
        const res = await fetch(path, opts);
        if (res.status === 401) {
            // Session expired or not logged in — let the login overlay handle it.
            // dashboard.js checkAuth will show the overlay on next status poll.
            return null;
        }
        const json = await res.json().catch(() => null);
        if (!res.ok) throw new APIError(res.status, json);
        return json;
    } finally {
        clearTimeout(timer);
    }
}

export const get  = (path)       => request('GET',    path);
export const post = (path, body) => request('POST',   path, body);
export const put  = (path, body) => request('PUT',    path, body);
export const del  = (path, body) => request('DELETE', path, body);

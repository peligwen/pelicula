// api.js — unified API client for the pelicula dashboard.
//
// All requests are sent with same-origin credentials and a 10-second timeout.
// A 401 from a /api/pelicula/ path means "session expired or not logged in"
// — the login overlay handles that case, so those 401s resolve to null
// instead of throwing. A 401 from any other path (e.g. /api/procula/*,
// /api/vpn/*, proxied directly by nginx) means a caller/auth-wiring bug, not
// session expiry, so it throws APIError like any other non-2xx response —
// see FE-6 in .claude/plans/audit-2026-07/frontend-ux.md.
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
        if (res.status === 401 && path.startsWith('/api/pelicula/')) {
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

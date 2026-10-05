// Shared helpers for the Pelicula specs. Not a spec itself (lives outside specs/).
//
// Most setup goes through the HTTP API instead of the UI: it is faster, and it
// keeps the number of logins low enough to stay under nginx's login rate limit
// (10 requests per minute per IP). Each spec drives the UI only for what it is
// actually testing.
const { request } = require('@playwright/test');

const BASE_URL = process.env.PELICULA_URL || 'http://localhost:7399';
const ADMIN_USER = process.env.PELICULA_ADMIN_USER || 'admin';

function adminPassword() {
  const pw = process.env.PELICULA_ADMIN_PASSWORD;
  if (!pw) {
    throw new Error('PELICULA_ADMIN_PASSWORD is not set (the Jellyfin admin password from .env)');
  }
  return pw;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// POST JSON, retrying while nginx rate limits us (503 by default, 429 if configured).
async function postJSON(ctx, path, data, { attempts = 8 } = {}) {
  let res;
  for (let i = 0; i < attempts; i++) {
    res = await ctx.post(path, { data });
    if (res.status() !== 429 && res.status() !== 503) return res;
    await sleep(7_000); // 10 r/min is one request per 6 s
  }
  return res;
}

// A fresh, cookie-holding API context. Dispose it when done.
async function newAPI() {
  return request.newContext({ baseURL: BASE_URL });
}

// API context signed in as the admin.
async function adminAPI() {
  const ctx = await newAPI();
  const res = await postJSON(ctx, '/api/auth/login', {
    username: ADMIN_USER,
    password: adminPassword(),
  });
  if (!res.ok()) {
    throw new Error(`admin login failed: ${res.status()} ${await res.text()}`);
  }
  return ctx;
}

// A browser context that carries the session of an API context, so a spec can
// open the dashboard already signed in without going through the login form.
async function browserContextFor(browser, apiCtx) {
  const storageState = await apiCtx.storageState();
  return browser.newContext({ baseURL: BASE_URL, storageState });
}

// Creates a viewer through the invite API. Returns { username, password, api },
// where api is a signed-in API context for that viewer.
async function createViewer(adminApi) {
  const inv = await postJSON(adminApi, '/api/invites', { role: 'viewer', expires_hours: 1 });
  if (inv.status() !== 201) {
    throw new Error(`create invite failed: ${inv.status()} ${await inv.text()}`);
  }
  const { code } = await inv.json();

  const username = uniqueUsername('pw');
  const password = uniquePassword();
  const api = await newAPI();
  const reg = await postJSON(api, '/api/register', { code, username, password });
  if (reg.status() !== 201) {
    throw new Error(`register failed: ${reg.status()} ${await reg.text()}`);
  }
  return { username, password, api };
}

// Best-effort removal of a user created by a spec (Jellyfin account + role).
async function deleteUser(adminApi, username) {
  try {
    await adminApi.delete(`/api/users/${encodeURIComponent(username)}`);
  } catch (_) {
    // the stack may already be gone; nothing to clean up then
  }
}

// ^[A-Za-z0-9._-]{3,32}$
function uniqueUsername(prefix) {
  return `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
}

function uniquePassword() {
  return `pw-${Math.random().toString(36).slice(2)}${Math.random().toString(36).slice(2)}`;
}

// Text of an element that may be an <input>/<textarea> (value) or any other
// element (text content).
async function valueOrText(locator) {
  return locator.evaluate((el) =>
    el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement
      ? el.value
      : (el.textContent || '').trim(),
  );
}

module.exports = {
  BASE_URL,
  ADMIN_USER,
  adminPassword,
  postJSON,
  newAPI,
  adminAPI,
  browserContextFor,
  createViewer,
  deleteUser,
  uniqueUsername,
  uniquePassword,
  valueOrText,
};

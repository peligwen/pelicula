// Shared helpers for the Pelicula specs. Not a spec itself (lives outside specs/).
//
// Setup goes through the HTTP API instead of the UI: it is faster, and it
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

module.exports = {
  BASE_URL,
  ADMIN_USER,
  adminPassword,
  postJSON,
  newAPI,
  adminAPI,
  browserContextFor,
};

# Playwright specs

Two browser specs for the dashboard. They run against an already running stack; they do not start one.

| Spec | What it checks |
|---|---|
| `specs/login.spec.js` | a bad password shows an inline error; the admin password shows the three tabs |
| `specs/search-add.spec.js` | the admin searches for a title and adds it; the card flips to *In library* and the API agrees. Skips when the metadata lookup is offline |

## Running

```bash
cd tests/playwright
npm install
npx playwright install chromium

export PELICULA_URL=http://localhost:7399        # default; use http://localhost:7354 for a real install
export PELICULA_ADMIN_USER=admin                 # default
export PELICULA_ADMIN_PASSWORD='<JELLYFIN_PASSWORD from .env>'
npm test                                         # or: make playwright (from the repo root)
```

`PELICULA_CHROMIUM_PATH` points Playwright at a specific Chromium binary; it is only used when `PLAYWRIGHT_BROWSERS_PATH` is unset.

The session cookie from one API login is copied into the browser context, so the specs sign in through the form only where that is the thing under test, which keeps them under nginx's login rate limit. The add spec puts a real title into Radarr or Sonarr on the stack it runs against.

## `data-testid` contract

The frontend must expose these exact ids. The specs select with `getByTestId`, which reads `data-testid`.

| Id | Element | Used by |
|---|---|---|
| `login-username` | username input in the login overlay | login |
| `login-password` | password input in the login overlay | login |
| `login-submit` | submit button of the login form | login |
| `login-error` | inline error text, visible after a failed login, non-empty | login |
| `tab-search` | Search tab button | all |
| `tab-jobs` | Jobs tab button | login |
| `tab-settings` | Settings tab button | login |
| `search-input` | the search text input (Enter submits) | search-add |
| `result-card` | one result card, with the title in `data-title`; repeated per result | search-add |
| `result-add` | the "Add" button inside a result card; repeated per card, absent on in-library cards | search-add |
| `result-in-library` | the "In library" badge that replaces `result-add` once a title is added | search-add |

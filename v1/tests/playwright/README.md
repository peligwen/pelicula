# Playwright specs

Three browser specs for the dashboard. They run against an already running stack; they do not start one.

| Spec | What it checks |
|---|---|
| `specs/login.spec.js` | a bad password shows an inline error; the admin password shows the tabs |
| `specs/invite-register.spec.js` | admin creates an invite in Settings; a fresh browser context opens the link, registers, and lands on the dashboard as a viewer with no Settings tab |
| `specs/request-approve.spec.js` | a viewer searches and requests a title; the admin approves it; the request shows approved. Skips when the metadata lookup is offline |

## Running

```bash
cd tests/playwright
npm install
npx playwright install chromium

export PELICULA_URL=http://localhost:7399        # default; use http://localhost:7354 for a real install
export PELICULA_ADMIN_USER=admin                 # default
export PELICULA_ADMIN_PASSWORD='<JELLYFIN_PASSWORD from .env>'
npm test                                         # or: make playwright (from v1/)
```

`PELICULA_CHROMIUM_PATH` points Playwright at a specific Chromium binary; it is only used when `PLAYWRIGHT_BROWSERS_PATH` is unset.

The specs create viewers through the invite API (`/api/invites`, `/api/register`) and delete them afterwards with `DELETE /api/users/{username}`. Session cookies from API logins are copied into browser contexts so the specs sign in through the form only where that is the thing under test, which keeps them under nginx's login rate limit.

## `data-testid` contract

The frontend must expose these exact ids. The specs select with `getByTestId`, which reads `data-testid`.

| Id | Element | Used by |
|---|---|---|
| `login-username` | username input in the login overlay | login |
| `login-password` | password input in the login overlay | login |
| `login-submit` | submit button of the login form | login |
| `login-error` | inline error text, visible after a failed login, non-empty | login |
| `tab-search` | Search tab button | all |
| `tab-requests` | Requests tab button | invite-register, request-approve |
| `tab-jobs` | Jobs tab button | login |
| `tab-settings` | Settings tab button; must be absent or hidden for non-admins | login, invite-register |
| `search-input` | the search text input (Enter submits) | request-approve |
| `search-result` | one result card; repeated per result | request-approve |
| `request-button` | the "Request" button inside a result card (viewers); repeated per card, absent on in-library cards | request-approve |
| `approve-button` | the "Approve" button in a pending request row, shown to managers and admins only; repeated per row | request-approve |
| `invite-create` | the "Create" button in the Settings invites section | invite-register |
| `invite-link` | element showing the full invite URL (`location.origin + path`) after creating; an `<input>` (its value is read) or any element (its text is read). If several exist the first is used, so the newest should come first | invite-register |
| `register-username` | username input on `/register` | invite-register |
| `register-password` | password input on `/register` | invite-register |
| `register-confirm` | confirm-password input on `/register` | invite-register |
| `register-submit` | submit button on `/register`; a 201 redirects to `/` | invite-register |

Two additions beyond the agreed list, needed to tell requests apart and read their state without depending on markup or CSS classes:

| Id | Element | Used by |
|---|---|---|
| `request-row` | one request in the Requests tab; contains the title text, a `request-status` and, for managers on pending rows, an `approve-button`. Repeated per request | request-approve |
| `request-status` | the status pill inside a `request-row`; its text is the status word: `pending`, `approved`, `declined` or `available` | request-approve |

If the frontend prefers other markup, change the two ids in `specs/request-approve.spec.js` rather than the spec flow.

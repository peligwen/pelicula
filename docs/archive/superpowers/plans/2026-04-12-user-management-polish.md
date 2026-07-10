# User Management Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the user management surface: replace `alert()` calls with inline errors, add an operator role dashboard, add disable/enable toggle for Jellyfin users, add per-user Movies/TV library access checkboxes, and relax the `/sessions` guard once role management is live.

**Architecture:** Four independent features that all touch `middleware/jellyfin.go` (user operations) and `nginx/users.js` / `nginx/index.html` (the Users tab). A new `middleware/operators.go` handles role CRUD against the `peligrosa.RolesStore`. The `JellyfinUser` struct gains `IsDisabled`, `EnableAllFolders`, and `EnabledFolders` fields populated from the policy block in `ListJellyfinUsers`. New Jellyfin helper functions `SetJellyfinUserDisabled` and `SetJellyfinUserLibraryAccess` follow the established pattern (GET full policy → merge field → POST back).

**Tech Stack:** Go stdlib, Jellyfin REST API, vanilla JS, existing `peligrosa.RolesStore`

---

### File Map

| File | Change |
|------|--------|
| `middleware/peligrosa/roles.go` | Add `Delete(jellyfinID string) error` method to `RolesStore` |
| `middleware/operators.go` | New file: `handleOperators` (GET list) and `handleOperatorsWithID` (POST set role / DELETE remove) |
| `middleware/jellyfin.go` | Add `IsDisabled`, `EnableAllFolders`, `EnabledFolders` to `JellyfinUser`; populate in `ListJellyfinUsers`; add `SetJellyfinUserDisabled`, `SetJellyfinUserLibraryAccess`; dispatch `/disable`, `/enable`, `/library` in `handleUsersWithID` |
| `middleware/main.go` | Register `/api/pelicula/operators` and `/api/pelicula/operators/` routes; relax `/sessions` to `GuardAuthenticated` |
| `nginx/index.html` | Add Operators section to Users tab |
| `nginx/users.js` | Replace `alert()` with inline errors; add `loadOperators`, role assignment, operator removal; add disable/enable handlers; add library access handlers |

---

### Task 1: Inline errors — replace `alert()` in `users.js`

**Files:**
- Modify: `nginx/users.js`

There are five `alert()` calls to replace: `deleteUser`, `submitResetPassword`, `approveRequest`, `revokeInvite`, `deleteInvite`. Each needs an error element scoped to the relevant list item or action area.

- [ ] **Step 1: Update the user list item template in `loadUsers()` to include an error element**

In `nginx/users.js`, find the template literal inside `loadUsers()` (the `html\`` block that builds each `<li>`). Add a `<span class="users-error hidden"></span>` as the last child before `</li>`:

```html
<span class="users-error hidden"></span>
```

The full `<li>` structure becomes: user-info div → user-actions div → user-reset-form → error span.

- [ ] **Step 2: Replace `alert()` in `deleteUser` with inline error**

Find the `if (!resp.ok)` block inside `deleteUser`. Replace the `alert()` call:

```js
        if (!resp.ok) {
            const data = await resp.json().catch(() => ({}));
            const errEl = li.querySelector('.users-error');
            if (errEl) { errEl.textContent = data.error || 'Failed to delete ' + name + '.'; errEl.classList.remove('hidden'); }
            btn.disabled = false;
            btn.dataset.confirming = '';
            btn.textContent = 'Delete';
            btn.classList.remove('user-action-delete-confirm');
            return;
        }
```

Also replace the `catch` block's `alert('Network error deleting user.')`:

```js
        } catch (e) {
            const errEl = li.querySelector('.users-error');
            if (errEl) { errEl.textContent = 'Network error deleting user.'; errEl.classList.remove('hidden'); }
            btn.disabled = false;
        }
```

- [ ] **Step 3: Replace `alert()` in `submitResetPassword` with inline error**

Find the failure path in `submitResetPassword`. Replace both `alert()` calls:

```js
        try {
            ...
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                const errEl = li.querySelector('.users-error');
                if (errEl) { errEl.textContent = data.error || 'Failed to reset password.'; errEl.classList.remove('hidden'); }
                return;
            }
            cancelResetPassword(btn);
        } catch (e) {
            const errEl = li.querySelector('.users-error');
            if (errEl) { errEl.textContent = 'Network error resetting password.'; errEl.classList.remove('hidden'); }
        } finally {
            btn.disabled = false;
        }
```

- [ ] **Step 4: Replace `alert()` in invite functions with inline errors**

The invite list item template (inside `loadInvites()`) needs an error element added to each `<li>` template, as the last child before `</li>`:

```html
<span class="invite-error hidden" style="font-size:0.75rem;color:var(--danger,#ff6b8a);padding:0.2rem 0;display:block"></span>
```

In `revokeInvite`, replace:

```js
            // Before:
            alert(d.error || 'Failed to revoke invite.');
            btn.disabled = false;

            // After:
            const errEl2 = li.querySelector('.invite-error');
            if (errEl2) { errEl2.textContent = d.error || 'Failed to revoke invite.'; errEl2.classList.remove('hidden'); }
            btn.disabled = false;
```

In `deleteInvite`, replace similarly:

```js
            // Before:
            alert(d.error || 'Failed to delete invite.');

            // After:
            const errEl3 = li.querySelector('.invite-error');
            if (errEl3) { errEl3.textContent = d.error || 'Failed to delete invite.'; errEl3.classList.remove('hidden'); }
```

- [ ] **Step 5: Browser verify**

Open the Users tab. Trigger an error (e.g. try to delete the only admin account). Confirm no browser `alert()` dialog appears — the error message shows inline in the list item row.

- [ ] **Step 6: Commit**

```bash
git add nginx/users.js
git commit -m "fix(users): replace alert() with inline errors in users.js"
```

---

### Task 2: Add `RolesStore.Delete` method

**Files:**
- Modify: `middleware/peligrosa/roles.go`

`Delete` is needed by the operator dashboard to remove a user's Pelicula role without touching their Jellyfin account.

- [ ] **Step 1: Write failing test**

Create `middleware/peligrosa/roles_test.go`:

```go
package peligrosa_test

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
	"pelicula-api/peligrosa"
)

func newTestRolesDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE roles (
		jellyfin_id TEXT PRIMARY KEY,
		username    TEXT NOT NULL,
		role        TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRolesStoreDelete(t *testing.T) {
	db := newTestRolesDB(t)
	rs := peligrosa.NewRolesStore(db)

	if err := rs.Upsert("user-1", "alice", peligrosa.RoleViewer); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, found := rs.Lookup("user-1"); !found {
		t.Fatal("expected entry after upsert")
	}
	if err := rs.Delete("user-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found := rs.Lookup("user-1"); found {
		t.Fatal("expected entry gone after delete")
	}
	// Deleting a non-existent ID should not error
	if err := rs.Delete("no-such-id"); err != nil {
		t.Fatalf("delete non-existent: %v", err)
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

```
cd middleware && go test ./peligrosa/... -run TestRolesStoreDelete -v
```
Expected: FAIL — `rs.Delete` undefined

- [ ] **Step 3: Add `Delete` to `RolesStore`**

In `middleware/peligrosa/roles.go`, add after the `All()` method:

```go
// Delete removes the role entry for jellyfinID. No-ops silently if the ID is
// not in the table.
func (rs *RolesStore) Delete(jellyfinID string) error {
	_, err := rs.db.Exec(`DELETE FROM roles WHERE jellyfin_id = ?`, jellyfinID)
	return err
}
```

- [ ] **Step 4: Run test to confirm it passes**

```
cd middleware && go test ./peligrosa/... -run TestRolesStoreDelete -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd middleware
git add peligrosa/roles.go peligrosa/roles_test.go
git commit -m "feat(roles): add RolesStore.Delete method"
```

---

### Task 3: Add operator CRUD backend

**Files:**
- Create: `middleware/operators.go`
- Modify: `middleware/main.go` (register routes)

- [ ] **Step 1: Write failing tests**

Create `middleware/operators_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleOperatorsGetNilStore(t *testing.T) {
	// authMiddleware is nil in package-level tests; handleOperators must return []
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/operators", nil)
	w := httptest.NewRecorder()
	handleOperators(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Must be a JSON array (even if empty), not null
	body := w.Body.String()
	if body == "null\n" || body == "null" {
		t.Error("expected [] not null")
	}
}

func TestHandleOperatorsWithID_InvalidRole(t *testing.T) {
	body, _ := json.Marshal(map[string]string{"role": "superadmin", "username": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/operators/abc-abc-abc-abc-abcabc", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleOperatorsWithID(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
```

- [ ] **Step 2: Run to confirm they fail**

```
cd middleware && go test ./... -run "TestHandleOperators" -v
```
Expected: FAIL — `handleOperators` and `handleOperatorsWithID` undefined

- [ ] **Step 3: Check `validJellyfinID` format requirements**

```
grep -n "validJellyfinID" middleware/jellyfin.go | head -5
```

The test IDs above must match whatever regex `validJellyfinID` accepts. If it requires a specific UUID-like format, adjust the test IDs accordingly.

- [ ] **Step 4: Create `middleware/operators.go`**

```go
package main

import (
	"encoding/json"
	"net/http"
	"pelicula-api/httputil"
	"pelicula-api/peligrosa"
	"strings"
)

// handleOperators handles GET /api/pelicula/operators — returns all role entries.
func handleOperators(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	store := authMiddleware.Roles()
	if store == nil {
		httputil.WriteJSON(w, []peligrosa.RolesEntry{})
		return
	}
	httputil.WriteJSON(w, store.All())
}

// handleOperatorsWithID handles POST /api/pelicula/operators/{id} (set role)
// and DELETE /api/pelicula/operators/{id} (remove entry).
func handleOperatorsWithID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/pelicula/operators/")
	if !validJellyfinID(id) {
		httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
		return
	}
	store := authMiddleware.Roles()
	if store == nil {
		httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
		return
	}
	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Role     peligrosa.UserRole `json:"role"`
			Username string             `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		switch req.Role {
		case peligrosa.RoleViewer, peligrosa.RoleManager, peligrosa.RoleAdmin:
			// valid
		default:
			httputil.WriteError(w, "role must be viewer, manager, or admin", http.StatusBadRequest)
			return
		}
		if err := store.Upsert(id, req.Username, req.Role); err != nil {
			httputil.WriteError(w, "could not update role", http.StatusInternalServerError)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
	case http.MethodDelete:
		if err := store.Delete(id); err != nil {
			httputil.WriteError(w, "could not remove role", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
```

- [ ] **Step 5: Run tests to confirm they pass**

```
cd middleware && go test ./... -run "TestHandleOperators" -v
```
Expected: PASS

- [ ] **Step 6: Register routes in `main.go`**

In `middleware/main.go`, after the users routes (after line ~167), add:

```go
	// admin only: role management (list + set + delete)
	mux.Handle("/api/pelicula/operators", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleOperators))))
	mux.Handle("/api/pelicula/operators/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleOperatorsWithID))))
```

- [ ] **Step 7: Build to confirm it compiles**

```
cd middleware && go build ./...
```
Expected: success

- [ ] **Step 8: Commit**

```bash
cd middleware
git add operators.go operators_test.go main.go
git commit -m "feat(operators): add role CRUD endpoints GET/POST/DELETE /api/pelicula/operators"
```

---

### Task 4: Operator dashboard frontend

**Files:**
- Modify: `nginx/index.html` — add Operators section to Users tab
- Modify: `nginx/users.js` — add `loadOperators`, role dropdown, remove handler

The Operators section shows all Jellyfin users with their current Pelicula role. It fetches `/api/pelicula/users` and `/api/pelicula/operators` in parallel, cross-references by ID, and renders each user with a role dropdown (viewer / manager / admin / — no access —). Selecting a role saves immediately; selecting "no access" removes the entry.

- [ ] **Step 1: Add Operators section HTML to `index.html`**

In `nginx/index.html`, inside the `users-section` div, after the `</div>` that closes the `um-lanes` div, add:

```html
                <!-- Operators (admin-only: Pelicula role assignment) -->
                <div class="admin-only" id="operators-section" style="margin-top:1.5rem">
                    <div class="um-lane-header" style="margin-bottom:0.5rem">
                        <span>Operators</span>
                        <span class="um-lane-hint">Pelicula roles for Jellyfin accounts</span>
                    </div>
                    <p class="users-hint">Assign roles to control dashboard access. <strong>Admin</strong>: manage everything. <strong>Manager</strong>: search + approve requests. <strong>Viewer</strong>: browse + request.</p>
                    <ul id="operators-list" class="users-list"></ul>
                    <p id="operators-empty" class="hidden" style="font-size:0.8rem;color:var(--muted);padding:0.5rem 0">No roles assigned yet.</p>
                    <p id="operators-error" class="users-error hidden"></p>
                </div>
```

- [ ] **Step 2: Add `loadOperators` to `users.js`**

Use `document.createElement` + `replaceChildren` (matching the `renderLogs` pattern, avoids XSS risk of unescaped markup):

```js
    async function loadOperators() {
        const list   = document.getElementById('operators-list');
        const empty  = document.getElementById('operators-empty');
        const errEl  = document.getElementById('operators-error');
        if (!list) return;
        try {
            const [usersResp, rolesResp] = await Promise.all([
                fetch('/api/pelicula/users'),
                fetch('/api/pelicula/operators'),
            ]);
            if (!usersResp.ok || !rolesResp.ok) {
                if (errEl) { errEl.textContent = 'Failed to load operators.'; errEl.classList.remove('hidden'); }
                return;
            }
            const users = await usersResp.json();
            const roles = await rolesResp.json();
            const roleMap = {};
            (roles || []).forEach(r => { roleMap[r.jellyfin_id] = r.role; });

            if (!users || users.length === 0) {
                if (empty) empty.classList.remove('hidden');
                list.replaceChildren();
                return;
            }
            if (empty) empty.classList.add('hidden');

            const frag = document.createDocumentFragment();
            users.forEach(u => {
                const li = document.createElement('li');
                li.dataset.userId   = u.id;
                li.dataset.userName = u.name;
                li.className = 'users-list-item'; // reuse existing style if present

                const info = document.createElement('div');
                info.className = 'user-info';
                const nameSpan = document.createElement('span');
                nameSpan.className = 'user-name';
                nameSpan.textContent = u.name;
                info.appendChild(nameSpan);

                const actions = document.createElement('div');
                actions.className = 'user-actions';
                actions.style.gap = '0.5rem';

                const select = document.createElement('select');
                select.className = 'operator-role-select';
                const currentRole = roleMap[u.id] || '';
                [['', '\u2014 no access \u2014'], ['viewer', 'viewer'], ['manager', 'manager'], ['admin', 'admin']].forEach(([val, label]) => {
                    const opt = document.createElement('option');
                    opt.value = val;
                    opt.textContent = label;
                    if (currentRole === val) opt.selected = true;
                    select.appendChild(opt);
                });
                select.addEventListener('change', () => setOperatorRole(select));
                actions.appendChild(select);

                if (currentRole) {
                    const removeBtn = document.createElement('button');
                    removeBtn.className = 'user-action-btn user-action-delete';
                    removeBtn.title = 'Remove role';
                    removeBtn.textContent = 'Remove';
                    removeBtn.addEventListener('click', () => removeOperator(removeBtn));
                    actions.appendChild(removeBtn);
                }

                const errSpan = document.createElement('span');
                errSpan.className = 'users-error hidden';

                li.appendChild(info);
                li.appendChild(actions);
                li.appendChild(errSpan);
                frag.appendChild(li);
            });
            list.replaceChildren(frag);
        } catch (e) {
            if (errEl) { errEl.textContent = 'Network error loading operators.'; errEl.classList.remove('hidden'); }
        }
    }
```

- [ ] **Step 3: Add `setOperatorRole` and `removeOperator`**

```js
    async function setOperatorRole(select) {
        const li    = select.closest('li');
        const id    = li.dataset.userId;
        const name  = li.dataset.userName;
        const role  = select.value;
        const errEl = li.querySelector('.users-error');
        if (errEl) errEl.classList.add('hidden');
        if (!role) {
            await doRemoveOperator(id, name, li);
            return;
        }
        try {
            const resp = await fetch('/api/pelicula/operators/' + encodeURIComponent(id), {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ role, username: name }),
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                if (errEl) { errEl.textContent = data.error || 'Failed to set role.'; errEl.classList.remove('hidden'); }
                return;
            }
            loadOperators();
        } catch (e) {
            if (errEl) { errEl.textContent = 'Network error.'; errEl.classList.remove('hidden'); }
        }
    }

    async function removeOperator(btn) {
        const li = btn.closest('li');
        await doRemoveOperator(li.dataset.userId, li.dataset.userName, li);
    }

    async function doRemoveOperator(id, name, li) {
        const errEl = li ? li.querySelector('.users-error') : null;
        try {
            const resp = await fetch('/api/pelicula/operators/' + encodeURIComponent(id), { method: 'DELETE' });
            if (resp.ok || resp.status === 204) {
                loadOperators();
                return;
            }
            const data = await resp.json().catch(() => ({}));
            if (errEl) { errEl.textContent = data.error || 'Failed to remove role for ' + name + '.'; errEl.classList.remove('hidden'); }
        } catch (e) {
            if (errEl) { errEl.textContent = 'Network error.'; errEl.classList.remove('hidden'); }
        }
    }
```

- [ ] **Step 4: Export and wire up `loadOperators`**

Add to the window exports block:
```js
window.loadOperators   = loadOperators;
window.setOperatorRole = setOperatorRole;
window.removeOperator  = removeOperator;
```

Check `nginx/dashboard.js` for where `loadUsers` / `loadInvites` are called on Users tab activation:
```
grep -n "loadUsers\|loadInvites\|loadSessions" nginx/dashboard.js
```

Add `if (window.loadOperators) window.loadOperators()` in the same location so operators load when the tab opens.

- [ ] **Step 5: Browser verify**

Open the Users tab as admin. Confirm:
- Operators section shows all Jellyfin users with role dropdowns
- Selecting "admin" for a user saves and shows a Remove button
- Changing back to "— no access —" removes the entry and hides the Remove button

- [ ] **Step 6: Commit**

```bash
git add nginx/index.html nginx/users.js nginx/dashboard.js
git commit -m "feat(operators): add operator role dashboard to Users tab"
```

---

### Task 5: Relax `/sessions` to `GuardAuthenticated`

**Files:**
- Modify: `middleware/main.go:172`

Now that viewer/manager roles can be managed via the operator dashboard, relax the sessions guard as noted in the code comment.

- [ ] **Step 1: Change the guard in `main.go`**

Find the sessions route (around line 172):

```go
// Before:
// GuardAdmin is intentionally conservative — the dashboard is admin-only today.
// Relax to GuardAuthenticated when viewer/manager roles land on the dashboard.
mux.Handle("/api/pelicula/sessions", auth.GuardAdmin(http.HandlerFunc(handleSessions)))

// After:
// viewer+: active Jellyfin sessions for the now-playing card.
mux.Handle("/api/pelicula/sessions", auth.Guard(http.HandlerFunc(handleSessions)))
```

- [ ] **Step 2: Build and run tests**

```
cd middleware && go build ./... && go test ./...
```
Expected: all PASS

- [ ] **Step 3: Commit**

```bash
cd middleware
git add main.go
git commit -m "chore(auth): relax /sessions from GuardAdmin to Guard (viewer+)"
```

---

### Task 6: Add disable/enable to Jellyfin user backend

**Files:**
- Modify: `middleware/jellyfin.go`

- [ ] **Step 1: Add `IsDisabled` to `JellyfinUser` struct**

In `middleware/jellyfin.go`, change `JellyfinUser` (line ~447):

```go
type JellyfinUser struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	HasPassword   bool   `json:"hasPassword"`
	IsAdmin       bool   `json:"isAdmin"`
	IsDisabled    bool   `json:"isDisabled"`
	LastLoginDate string `json:"lastLoginDate,omitempty"`
}
```

- [ ] **Step 2: Populate `IsDisabled` in `ListJellyfinUsers`**

In `ListJellyfinUsers`, inside the loop where `isAdmin` is read from `policy` (the `if policy, ok := u["Policy"]` block), add:

```go
		isAdmin, isDisabled := false, false
		if policy, ok := u["Policy"].(map[string]any); ok {
			isAdmin, _ = policy["IsAdministrator"].(bool)
			isDisabled, _ = policy["IsDisabled"].(bool)
		}
		users = append(users, JellyfinUser{
			ID:            id,
			Name:          name,
			HasPassword:   hasPass,
			IsAdmin:       isAdmin,
			IsDisabled:    isDisabled,
			LastLoginDate: lastLogin,
		})
```

- [ ] **Step 3: Write failing test for `SetJellyfinUserDisabled`**

Look at `middleware/jellyfin_test.go` for the mock Jellyfin httptest server fixture (see how existing tests set up `services` with a mock server). Add a test that:
1. Stubs `GET /Users/{id}` to return a user with a policy
2. Stubs `POST /Users/{id}/Policy` to record the posted body
3. Calls `SetJellyfinUserDisabled(services, id, true)`
4. Asserts the recorded body has `"IsDisabled": true`

Write the test body matching the existing test fixture in `jellyfin_test.go`.

Run: `cd middleware && go test ./... -run TestSetJellyfinUserDisabled -v`
Expected: FAIL — `SetJellyfinUserDisabled` undefined

- [ ] **Step 4: Add `SetJellyfinUserDisabled` helper**

In `middleware/jellyfin.go`, add after `SetJellyfinUserPassword`:

```go
// SetJellyfinUserDisabled enables or disables a Jellyfin user account.
// It GETs the full current policy, sets IsDisabled, then POSTs the entire
// policy back — Jellyfin replaces the full policy on POST, so partial updates
// would zero out other fields.
func SetJellyfinUserDisabled(s *ServiceClients, id string, disabled bool) error {
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	userData, err := jellyfinGet(s, "/Users/"+id, token)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	var user map[string]any
	if err := json.Unmarshal(userData, &user); err != nil {
		return fmt.Errorf("decode user: %w", err)
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	policy["IsDisabled"] = disabled
	if _, err := jellyfinPost(s, "/Users/"+id+"/Policy", token, policy); err != nil {
		return fmt.Errorf("post policy: %w", err)
	}
	action := "disabled"
	if !disabled {
		action = "enabled"
	}
	slog.Info("Jellyfin user account "+action, "component", "jellyfin", "userId", id)
	return nil
}
```

- [ ] **Step 5: Dispatch `/disable` and `/enable` in `handleUsersWithID`**

In `middleware/jellyfin.go`, in `handleUsersWithID`, add two suffix checks before the final `if !validJellyfinID(tail)` block (after the `/password` check):

```go
	if strings.HasSuffix(tail, "/disable") {
		id := strings.TrimSuffix(tail, "/disable")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := SetJellyfinUserDisabled(services, id, true); err != nil {
			slog.Error("disable user failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not disable user", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if strings.HasSuffix(tail, "/enable") {
		id := strings.TrimSuffix(tail, "/enable")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := SetJellyfinUserDisabled(services, id, false); err != nil {
			slog.Error("enable user failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not enable user", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}
```

- [ ] **Step 6: Run full test suite**

```
cd middleware && go test ./...
```
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
cd middleware
git add jellyfin.go jellyfin_test.go
git commit -m "feat(users): add IsDisabled to JellyfinUser; add SetJellyfinUserDisabled and /disable /enable endpoints"
```

---

### Task 7: Disable/enable toggle in user list frontend

**Files:**
- Modify: `nginx/users.js`

- [ ] **Step 1: Add Disable/Enable button and disabled badge to the user list template**

In `loadUsers()`, update the `<li>` template. The `user-info` div gains a "disabled" badge when `u.isDisabled` is true. The `user-actions` div gains a Disable/Enable button:

Inside the existing user list template in `loadUsers()`, update the `html\`` block:

```js
            const disabledBadge = u.isDisabled
                ? '<span class="user-admin-badge" style="background:var(--danger-dim,#3a1a2a);color:var(--danger,#ff6b8a)">disabled</span>'
                : '';
            const disableBtn = `<button class="user-action-btn" onclick="toggleDisableUser(this)" data-disabled="${u.isDisabled ? 'true' : 'false'}" title="${u.isDisabled ? 'Re-enable account' : 'Disable account'}">${u.isDisabled ? 'Enable' : 'Disable'}</button>`;
```

Then insert `${raw(disabledBadge)}` in the user-info span (after `${raw(adminBadge)}`) and `${raw(disableBtn)}` in the user-actions div.

Note: `raw()` is the existing unsafe-string helper already used for `adminBadge`. The `disabledBadge` content is fully static HTML (no user data), so it's safe. The `disableBtn` interpolates only `u.isDisabled` (a boolean) and HTML-safe string literals — no user data.

- [ ] **Step 2: Add `toggleDisableUser` handler**

```js
    async function toggleDisableUser(btn) {
        const li        = btn.closest('li');
        const id        = li.dataset.userId;
        const isDisabled = btn.dataset.disabled === 'true';
        const action    = isDisabled ? 'enable' : 'disable';
        const errEl     = li.querySelector('.users-error');
        if (errEl) errEl.classList.add('hidden');
        btn.disabled = true;
        try {
            const resp = await fetch('/api/pelicula/users/' + encodeURIComponent(id) + '/' + action, { method: 'POST' });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                if (errEl) { errEl.textContent = data.error || 'Failed to ' + action + ' user.'; errEl.classList.remove('hidden'); }
                btn.disabled = false;
                return;
            }
            loadUsers();
        } catch (e) {
            if (errEl) { errEl.textContent = 'Network error.'; errEl.classList.remove('hidden'); }
            btn.disabled = false;
        }
    }
```

- [ ] **Step 3: Export `toggleDisableUser`**

```js
window.toggleDisableUser = toggleDisableUser;
```

- [ ] **Step 4: Browser verify**

Open the Users tab. Each user row shows a Disable button. Clicking Disable shows the "disabled" badge and changes the button to "Enable". Clicking Enable reverses it.

- [ ] **Step 5: Commit**

```bash
git add nginx/users.js
git commit -m "feat(users): add disable/enable toggle to user list"
```

---

### Task 8: Per-user library access backend

**Files:**
- Modify: `middleware/jellyfin.go`

- [ ] **Step 1: Add library fields to `JellyfinUser`**

Update the struct:

```go
type JellyfinUser struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	HasPassword      bool     `json:"hasPassword"`
	IsAdmin          bool     `json:"isAdmin"`
	IsDisabled       bool     `json:"isDisabled"`
	EnableAllFolders bool     `json:"enableAllFolders"`
	EnabledFolders   []string `json:"enabledFolders,omitempty"`
	LastLoginDate    string   `json:"lastLoginDate,omitempty"`
}
```

In `ListJellyfinUsers`, update the policy block to also read these fields:

```go
		isAdmin, isDisabled, enableAll := false, false, true
		var enabledFolders []string
		if policy, ok := u["Policy"].(map[string]any); ok {
			isAdmin, _ = policy["IsAdministrator"].(bool)
			isDisabled, _ = policy["IsDisabled"].(bool)
			enableAll, _ = policy["EnableAllFolders"].(bool)
			if raw, ok := policy["EnabledFolders"].([]any); ok {
				for _, f := range raw {
					if s, ok := f.(string); ok {
						enabledFolders = append(enabledFolders, s)
					}
				}
			}
		}
		users = append(users, JellyfinUser{
			ID:               id,
			Name:             name,
			HasPassword:      hasPass,
			IsAdmin:          isAdmin,
			IsDisabled:       isDisabled,
			EnableAllFolders: enableAll,
			EnabledFolders:   enabledFolders,
			LastLoginDate:    lastLogin,
		})
```

Note: Jellyfin defaults `EnableAllFolders` to `true` for new accounts, so newly created users will correctly show both boxes checked.

- [ ] **Step 2: Add `jellyfinLibraryIDs` and `SetJellyfinUserLibraryAccess`**

In `middleware/jellyfin.go`, add after `SetJellyfinUserDisabled`:

```go
// jellyfinLibraryIDs returns a map of library name → Jellyfin folder ID.
func jellyfinLibraryIDs(s *ServiceClients, token string) (map[string]string, error) {
	data, err := jellyfinGet(s, "/Library/VirtualFolders", token)
	if err != nil {
		return nil, err
	}
	var folders []struct {
		Name   string `json:"Name"`
		ItemId string `json:"ItemId"`
	}
	if err := json.Unmarshal(data, &folders); err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(folders))
	for _, f := range folders {
		ids[f.Name] = f.ItemId
	}
	return ids, nil
}

// SetJellyfinUserLibraryAccess patches the user's policy to control access to
// the "Movies" and "TV Shows" libraries. When both are true, EnableAllFolders
// is set to true. When partial, EnableAllFolders is false and EnabledFolders
// lists the selected library IDs.
func SetJellyfinUserLibraryAccess(s *ServiceClients, id string, movies, tv bool) error {
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	userData, err := jellyfinGet(s, "/Users/"+id, token)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	var user map[string]any
	if err := json.Unmarshal(userData, &user); err != nil {
		return fmt.Errorf("decode user: %w", err)
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}

	if movies && tv {
		policy["EnableAllFolders"] = true
		policy["EnabledFolders"] = []string{}
	} else {
		libIDs, err := jellyfinLibraryIDs(s, token)
		if err != nil {
			return fmt.Errorf("get library IDs: %w", err)
		}
		var folders []string
		if movies {
			if fid, ok := libIDs["Movies"]; ok {
				folders = append(folders, fid)
			}
		}
		if tv {
			if fid, ok := libIDs["TV Shows"]; ok {
				folders = append(folders, fid)
			}
		}
		policy["EnableAllFolders"] = false
		policy["EnabledFolders"] = folders
	}

	if _, err := jellyfinPost(s, "/Users/"+id+"/Policy", token, policy); err != nil {
		return fmt.Errorf("post policy: %w", err)
	}
	slog.Info("updated library access", "component", "jellyfin", "userId", id, "movies", movies, "tv", tv)
	return nil
}
```

- [ ] **Step 3: Dispatch `/library` in `handleUsersWithID`**

Add after the `/enable` block (before the final `if !validJellyfinID(tail)` check):

```go
	if strings.HasSuffix(tail, "/library") {
		id := strings.TrimSuffix(tail, "/library")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Movies bool `json:"movies"`
			TV     bool `json:"tv"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := SetJellyfinUserLibraryAccess(services, id, req.Movies, req.TV); err != nil {
			slog.Error("set library access failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not update library access", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}
```

- [ ] **Step 4: Run full test suite**

```
cd middleware && go build ./... && go test ./...
```
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
cd middleware
git add jellyfin.go
git commit -m "feat(users): add per-user library access (Movies/TV) via /library endpoint"
```

---

### Task 9: Per-user library access frontend

**Files:**
- Modify: `nginx/users.js`

- [ ] **Step 1: Add Movies/TV checkboxes to the user list template**

In `loadUsers()`, add checkbox variables computed from the user object:

```js
            const moviesOn = u.enableAllFolders || false;
            const tvOn     = u.enableAllFolders || false;
            // When enableAllFolders is false with a non-empty enabledFolders, the user
            // has custom folder access (set outside the dashboard). Both boxes show
            // unchecked as a safe default; saving will apply the chosen coarse access.
```

Then use these booleans in the `html\`` template to set the `checked` attribute on the checkbox inputs (use a ternary returning `'checked'` or `''`).

The library access row should be added inside the `<li>` as a plain text node row using DOM creation to avoid any markup injection risk. Add a `user-library-row` class for styling.

- [ ] **Step 2: Add `saveLibraryAccess` handler**

```js
    async function saveLibraryAccess(checkbox) {
        const li    = checkbox.closest('li');
        const id    = li.dataset.userId;
        const movies = li.querySelector('.user-lib-movies').checked;
        const tv     = li.querySelector('.user-lib-tv').checked;
        const errEl  = li.querySelector('.users-error');
        if (errEl) errEl.classList.add('hidden');
        try {
            const resp = await fetch('/api/pelicula/users/' + encodeURIComponent(id) + '/library', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ movies, tv }),
            });
            if (!resp.ok) {
                const data = await resp.json().catch(() => ({}));
                if (errEl) { errEl.textContent = data.error || 'Failed to update library access.'; errEl.classList.remove('hidden'); }
            }
        } catch (e) {
            if (errEl) { errEl.textContent = 'Network error.'; errEl.classList.remove('hidden'); }
        }
    }
```

- [ ] **Step 3: Export**

```js
window.saveLibraryAccess = saveLibraryAccess;
```

- [ ] **Step 4: Browser verify**

Open Users tab. Each user row shows Movies/TV checkboxes. For a user with `enableAllFolders: true`, both are checked. Unchecking Movies and saving restricts that user to TV Shows only in Jellyfin. Checking both restores full access.

- [ ] **Step 5: Commit**

```bash
git add nginx/users.js
git commit -m "feat(users): add Movies/TV library access checkboxes to user list"
```

---

## Self-Review

**Spec coverage:**

| Requirement | Covered |
|-------------|---------|
| Inline errors on delete/password-reset | ✓ Task 1 |
| Operator dashboard — UI for creating/editing roles | ✓ Tasks 3–4 |
| Disable/enable Jellyfin user | ✓ Tasks 6–7 |
| Per-user library access (Movies/TV checkboxes) | ✓ Tasks 8–9 |
| Relax /sessions to GuardAuthenticated | ✓ Task 5 |
| Magic-link invite | ✗ Deferred per issue |
| Structured userFacingError type | ✗ Deferred — no established pattern |
| Playback history per user | ✗ Deferred per issue ("heavier UI") |
| Password sync operator↔Jellyfin | ✗ Deferred per issue |

**Type consistency:**
- `JellyfinUser.IsDisabled bool` added in Task 6, consumed in Task 7 — ✓
- `JellyfinUser.EnableAllFolders bool` + `EnabledFolders []string` added in Task 8, consumed in Task 9 — ✓
- `SetJellyfinUserDisabled(s, id, bool)` defined Task 6, dispatched Task 6 Step 5 — ✓
- `SetJellyfinUserLibraryAccess(s, id, movies, tv bool)` defined Task 8, dispatched Task 8 Step 3 — ✓
- `RolesStore.Delete(id)` defined Task 2, called in `operators.go` Task 3 — ✓
- `/api/pelicula/operators` routes registered Task 3, UI calls them in Task 4 — ✓

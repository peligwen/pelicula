package api

import (
	"testing"

	"pelicula/internal/store"
)

func seedUsers(e *env) {
	e.jellyfin.users = []JellyfinUser{
		{ID: "u1", Name: "adam", IsAdmin: true, LastLogin: "2026-10-01T10:00:00Z"},
		{ID: "u2", Name: "mona", LastLogin: "2026-10-02T10:00:00Z"},
		{ID: "u3", Name: "Vera", IsDisabled: true},
		{ID: "u4", Name: "victor"},
	}
}

func TestListUsersJoinsRoles(t *testing.T) {
	e := newEnv(t)
	seedUsers(e)
	if err := e.store.SetRole(t.Context(), "mona", store.RoleManager); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetRole(t.Context(), "victor", store.RoleAdmin); err != nil { // explicit beats default
		t.Fatal(err)
	}

	rec := e.do(admin, "GET", "/api/users", nil)
	wantStatus(t, rec, 200)
	byName := map[string]map[string]any{}
	for _, raw := range decode(t, rec)["users"].([]any) {
		u := raw.(map[string]any)
		byName[u["name"].(string)] = u
	}
	if len(byName) != 4 {
		t.Fatalf("users = %v", byName)
	}
	roles := map[string]string{"adam": "admin", "mona": "manager", "Vera": "viewer", "victor": "admin"}
	for name, role := range roles {
		if byName[name]["role"] != role {
			t.Errorf("%s role = %v, want %s", name, byName[name]["role"], role)
		}
	}
	if u := byName["adam"]; u["id"] != "u1" || u["is_admin"] != true || u["is_disabled"] != false || u["last_login"] != "2026-10-01T10:00:00Z" {
		t.Errorf("adam = %v", u)
	}
	if byName["Vera"]["is_disabled"] != true {
		t.Errorf("Vera = %v", byName["Vera"])
	}
	wantStatus(t, e.do(manager, "GET", "/api/users", nil), 403)
}

func TestListUsersWithoutJellyfin(t *testing.T) {
	e := newEnv(t)
	e.srv.Jellyfin = nil
	wantStatus(t, e.do(admin, "GET", "/api/users", nil), 503)
}

func TestSetUserRole(t *testing.T) {
	e := newEnv(t)
	seedUsers(e)

	wantStatus(t, e.do(admin, "PUT", "/api/users/mona/role", map[string]any{"role": "admin"}), 204)
	if role, ok, _ := e.store.GetRole(t.Context(), "mona"); !ok || role != store.RoleAdmin {
		t.Fatalf("role = %q ok=%v", role, ok)
	}

	// The role is stored under Jellyfin's own spelling of the name.
	wantStatus(t, e.do(admin, "PUT", "/api/users/vera/role", map[string]any{"role": "manager"}), 204)
	if role, ok, _ := e.store.GetRole(t.Context(), "Vera"); !ok || role != store.RoleManager {
		t.Fatalf("role for Vera = %q ok=%v", role, ok)
	}

	// Live sessions follow the new role immediately (store.SetRole does it).
	if err := e.store.CreateSession(t.Context(), store.Session{Token: "t", Username: "victor", Role: store.RoleViewer, ExpiresAt: farFuture()}); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.do(admin, "PUT", "/api/users/victor/role", map[string]any{"role": "manager"}), 204)
	if sess, _ := e.store.GetSession(t.Context(), "t"); sess == nil || sess.Role != store.RoleManager {
		t.Fatalf("session = %+v", sess)
	}
}

func TestSetUserRoleRejections(t *testing.T) {
	e := newEnv(t)
	seedUsers(e)

	// Self-protection: an admin cannot change their own role (any spelling).
	wantStatus(t, e.do(admin, "PUT", "/api/users/adam/role", map[string]any{"role": "viewer"}), 400)
	wantStatus(t, e.do(admin, "PUT", "/api/users/ADAM/role", map[string]any{"role": "viewer"}), 400)
	if _, ok, _ := e.store.GetRole(t.Context(), "adam"); ok {
		t.Fatal("self role change was stored")
	}

	wantStatus(t, e.do(admin, "PUT", "/api/users/mona/role", map[string]any{"role": "emperor"}), 400)
	wantStatus(t, e.do(admin, "PUT", "/api/users/mona/role", map[string]any{}), 400)
	wantStatus(t, e.do(admin, "PUT", "/api/users/mona/role", "{"), 400)
	wantStatus(t, e.do(admin, "PUT", "/api/users/ghost/role", map[string]any{"role": "viewer"}), 404)
	wantStatus(t, e.do(manager, "PUT", "/api/users/vera/role", map[string]any{"role": "viewer"}), 403)
	if roles, _ := e.store.ListRoles(t.Context()); len(roles) != 0 {
		t.Fatalf("roles stored by rejected calls: %v", roles)
	}
}

func TestDeleteUser(t *testing.T) {
	e := newEnv(t)
	seedUsers(e)
	ctx := t.Context()
	if err := e.store.SetRole(ctx, "mona", store.RoleManager); err != nil {
		t.Fatal(err)
	}
	if err := e.store.CreateSession(ctx, store.Session{Token: "mona-tok", Username: "mona", Role: store.RoleManager, ExpiresAt: farFuture()}); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, e.do(admin, "DELETE", "/api/users/mona", nil), 204)
	if len(e.jellyfin.deleted) != 1 || e.jellyfin.deleted[0] != "u2" {
		t.Fatalf("Jellyfin deletes = %v, want [u2]", e.jellyfin.deleted)
	}
	if _, ok, _ := e.store.GetRole(ctx, "mona"); ok {
		t.Error("role row survived the delete")
	}
	if sess, _ := e.store.GetSession(ctx, "mona-tok"); sess != nil {
		t.Error("session survived the delete")
	}

	wantStatus(t, e.do(admin, "DELETE", "/api/users/ghost", nil), 404)
	wantStatus(t, e.do(manager, "DELETE", "/api/users/victor", nil), 403)
}

func TestDeleteUserSelfProtection(t *testing.T) {
	e := newEnv(t)
	seedUsers(e)
	wantStatus(t, e.do(admin, "DELETE", "/api/users/adam", nil), 400)
	wantStatus(t, e.do(admin, "DELETE", "/api/users/Adam", nil), 400)
	if len(e.jellyfin.deleted) != 0 {
		t.Fatalf("self-delete reached Jellyfin: %v", e.jellyfin.deleted)
	}
}

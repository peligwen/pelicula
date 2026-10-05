// Package integration holds the end-to-end test that starts the real Docker
// Compose stack and drives it over HTTP.
//
// The test lives in stack_test.go behind the "integration" build tag, so
// plain `go test ./...` and `go vet ./...` ignore it and need no Docker. Run
// it with:
//
//	make e2e
//	# or
//	go test -tags integration -count=1 -timeout 20m ./tests/integration/...
//
// It starts compose/docker-compose.yml as project "pelicula-test" on port
// 7399 without the vpn profile (PELICULA_VPN=false), using temporary
// CONFIG_DIR, LIBRARY_DIR and WORK_DIR folders, and always tears the stack
// down with `docker compose down -v --remove-orphans`.
//
// The test does not run the pelicula CLI, so it writes the minimum service
// seeds itself (the *arr UrlBase and Jellyfin BaseUrl that the CLI normally
// seeds). Keep that in sync with cmd/pelicula/seed.go.
//
// Optional environment variables:
//
//	PELICULA_E2E_REQUIRE_SEARCH=1  fail instead of skipping the search,
//	                               request and approve steps when the
//	                               Radarr/Sonarr lookup is unavailable
//	                               (for example no internet to TMDB)
package integration

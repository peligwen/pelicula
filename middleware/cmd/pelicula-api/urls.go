package main

// proculaURL is the base URL for the Procula processing-pipeline service.
// Used by hooks, catalog, jobs, actions, and services health check. A var
// (not const) so tests can point it at an httptest.Server; the canonical
// value is read from the environment at startup.
var proculaURL = envOr("PROCULA_URL", "http://procula:8282")

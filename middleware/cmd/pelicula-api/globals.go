package main

// globals.go — package-level variables retained during the incremental migration
// from a flat package main to the internal/ subpackage structure.
//
// These vars are set by main() immediately after the App struct is constructed.
// New code should use App struct fields directly. These exist so that the ~40
// handler files that were written against package-level globals continue to
// compile and work correctly until each file is individually migrated.

import (
	"database/sql"

	"pelicula-api/internal/app/library"
	"pelicula-api/internal/clients/apprise"
	"pelicula-api/internal/clients/docker"
	"pelicula-api/internal/peligrosa"
)

// services is the package-level ServiceClients instance, used by all handler
// and helper functions that haven't been migrated to accept it as a parameter.
// Set by main() once constructed; never nil after startup.
var services *ServiceClients

// authMiddleware is the peligrosa Auth instance, used by operator/export handlers.
// Set by main() once constructed.
var authMiddleware *peligrosa.Auth

// inviteStore and requestStore mirror their peligrosa counterparts.
// Set by main() once constructed.
var inviteStore *peligrosa.InviteStore
var requestStore *peligrosa.RequestStore

// mainDB is the primary SQLite database handle (pelicula.db).
// Set by main() once opened.
var mainDB *sql.DB

// libHandler is the package-level library Handler, used by handler functions
// that haven't been migrated to use App struct fields directly.
// Set by main() once constructed; never nil after startup.
var libHandler *library.Handler

// dockerCli is the package-level Docker socket proxy client.
// Set by main() once constructed; never nil after startup.
var dockerCli *docker.Client

// appriseCli is the package-level Apprise notification client.
// Set by main() once constructed; never nil after startup.
var appriseCli *apprise.Client

// indexerCount is the package-level indexer count cache, used by autowire to
// invalidate the cached count after wiring completes.
var indexerCount indexerCountApp

// indexerCountApp is a minimal invalidation handle for the Prowlarr indexer count.
// Used by package-level code (notably autowire) that doesn't have access to App.
// The App's idxCache will expire via its TTL regardless, so invalidate is a no-op.
type indexerCountApp struct{}

func (c *indexerCountApp) invalidate() {}

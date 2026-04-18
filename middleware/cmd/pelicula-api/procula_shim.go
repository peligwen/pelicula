// procula_shim.go — compatibility shims for cmd/ code that was previously
// coupled to hooks_normalize.go and hooks_proxy.go.
//
// ProculaJobSource is re-exported from internal/app/catalog so that
// catalog_sync.go and library_apply.go don't need to change their import paths
// during the incremental migration.
package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"pelicula-api/internal/app/catalog"
)

// ProculaJobSource is a type alias for catalog.ProculaJobSource. Declared here
// so that cmd/ files that reference ProculaJobSource directly continue to compile
// without modification while the hooks package moves to internal/.
type ProculaJobSource = catalog.ProculaJobSource

// forwardToProcula creates a Procula processing job for the given source.
// Used by library_apply.go in addition to the hooks handler.
func forwardToProcula(ctx context.Context, source ProculaJobSource) error {
	if _, err := procClient.CreateJob(ctx, source); err != nil {
		return fmt.Errorf("reach procula: %w", err)
	}
	return nil
}

// isUnderPrefixes reports whether the cleaned path equals or is nested under
// one of the given prefixes. Previously defined in hooks_normalize.go; moved
// here because library_proxy.go and library_scan.go also use it.
func isUnderPrefixes(p string, prefixes []string) bool {
	clean := filepath.Clean(p)
	for _, prefix := range prefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

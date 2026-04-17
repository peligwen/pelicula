// Package repo provides typed data-access stores for pelicula-api.
// Each store wraps a *sql.DB and exposes named methods for the SQL operations
// used by peligrosa/. Time values are stored and parsed via dbutil.ParseTime /
// dbutil.FormatTime (RFC3339Nano with RFC3339 fallback).
//
// The peligrosa/ callers still use their own embedded SQL; this layer exists so
// future phases can migrate them incrementally without a big-bang rewrite.
package repo

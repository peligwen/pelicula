// Package actions provides the central action registry for procula's action bus.
// Actions are discrete operations on library items (validate, transcode,
// subtitle_refresh). Each action is registered with a Handler that runs inside
// the worker loop when a job's ActionType != "pipeline".
package actions

import (
	"context"
	"sort"
	"sync"

	"procula/internal/queue"
)

// Handler is the function signature for an action handler.
// It runs in the worker goroutine and returns a result map and an error.
type Handler func(ctx context.Context, q *queue.Queue, job *queue.Job) (map[string]any, error)

// Def describes one registered action.
type Def struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	AppliesTo   []string `json:"applies_to"`
	Sync        bool     `json:"sync"`
	Description string   `json:"description,omitempty"`
	Handler     Handler  `json:"-"`
}

// Registry holds the set of registered action definitions.
type Registry struct {
	mu      sync.RWMutex
	actions map[string]*Def
}

// New creates an empty Registry.
func New() *Registry {
	return &Registry{actions: make(map[string]*Def)}
}

// Register adds or replaces an action definition.
func (r *Registry) Register(def *Def) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions[def.Name] = def
}

// Lookup returns the Def for name, or nil if unknown.
func (r *Registry) Lookup(name string) *Def {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.actions[name]
}

// List returns all registered actions sorted by name.
func (r *Registry) List() []*Def {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Def, 0, len(r.actions))
	for _, d := range r.actions {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

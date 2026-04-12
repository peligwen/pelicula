// nginx/framework.js
// Pelicula micro-framework — no dependencies, no build step.
//
// API:
//   const store = createStore(initial)    — reactive state store
//   store.get(key)                        — read a value
//   store.set(key, value)                 — write + notify subscribers
//   store.subscribe(key, fn)              — fn(newValue) called on change
//   store.unsubscribe(key, fn)            — remove a subscription
//
//   component(name, factory)             — register a component
//   mount(name, el, props)               — mount a registered component into el
//
//   html`<div>${expr}</div>`             — tagged template: auto-escapes interpolations
//   raw(str)                             — mark a string as pre-escaped (trust it as-is)
//
// Design notes:
//   - store.set() is synchronous; subscribers are called immediately.
//   - Components are plain functions: factory(el, store, props) → { render, destroy }.
//     render() is called on mount and whenever a subscribed store key changes.
//   - html`` escapes string interpolations only; numbers/booleans pass through.
//     Use raw() to embed pre-escaped HTML strings.

'use strict';

// ── Store ─────────────────────────────────────────────────────────────────────

function createStore(initial) {
    const state  = Object.assign({}, initial);
    const subs   = {};   // key → Set<fn>
    let batchDepth   = 0;
    let batchPending = new Map();

    function get(key) {
        return state[key];
    }

    function set(key, value) {
        if (batchDepth > 0) { batchPending.set(key, value); return; }
        if (state[key] === value) return;
        state[key] = value;
        if (subs[key]) subs[key].forEach(fn => { try { fn(value); } catch(e) { console.error('[store]', e); } });
    }

    function subscribe(key, fn) {
        (subs[key] = subs[key] || new Set()).add(fn);
    }

    function unsubscribe(key, fn) {
        if (subs[key]) subs[key].delete(fn);
    }

    // Batch multiple set() calls without intermediate re-renders.
    // Usage: store.batch(() => { store.set('a',1); store.set('b',2); })
    function batch(fn) {
        batchDepth++;
        try { fn(); } finally {
            batchDepth--;
            if (batchDepth === 0) {
                const pending = batchPending;
                batchPending = new Map();
                for (const [key, value] of pending) set(key, value);
            }
        }
    }

    return { get, set, subscribe, unsubscribe, batch };
}

// ── Component registry ────────────────────────────────────────────────────────

const _registry  = {};   // name → factory fn
const _mounted   = [];   // { name, el, instance, unsubs }
const _loadedOnce = new Set();  // component names that have called loadOnce

function component(name, factory) {
    _registry[name] = factory;
}

function mount(name, el, props) {
    const factory = _registry[name];
    if (!factory) { console.error('[framework] Unknown component:', name); return; }
    const unsubs = [];
    const storeProxy = {
        get:    (key) => appStore.get(key),
        subscribe: (key, fn) => { appStore.subscribe(key, fn); unsubs.push(() => appStore.unsubscribe(key, fn)); },
        set:    (key, value) => appStore.set(key, value),
    };
    const instance = factory(el, storeProxy, props || {});
    _mounted.push({ name, el, instance, unsubs });
    if (instance && typeof instance.render === 'function') instance.render();
    if (instance && typeof instance.loadOnce === 'function' && !_loadedOnce.has(name)) {
        _loadedOnce.add(name);
        instance.loadOnce();
    }
    return instance;
}

function unmount(el) {
    const idx = _mounted.findIndex(m => m.el === el);
    if (idx === -1) return;
    const { instance, unsubs } = _mounted[idx];
    unsubs.forEach(fn => fn());
    if (instance && typeof instance.destroy === 'function') instance.destroy();
    _mounted.splice(idx, 1);
}

// ── html tagged template ──────────────────────────────────────────────────────

const _RAW = Symbol('raw');

function raw(str) {
    return { [_RAW]: true, str: String(str) };
}

function _escapeHtml(s) {
    if (s == null) return '';
    if (typeof s === 'number' || typeof s === 'boolean') return String(s);
    if (s && s[_RAW]) return s.str;
    return String(s)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

function html(strings, ...values) {
    let result = '';
    strings.forEach((str, i) => {
        result += str;
        if (i < values.length) {
            const v = values[i];
            if (Array.isArray(v)) {
                result += v.map(item => (item && item[_RAW]) ? item.str : _escapeHtml(item)).join('');
            } else {
                result += _escapeHtml(v);
            }
        }
    });
    return raw(result);
}

// ── Global store (singleton, shared by all components) ───────────────────────
// dashboard.js initialises this after framework.js loads.

let appStore = null;

function initStore(initial) {
    appStore = createStore(initial);
    return appStore;
}

// ── data-testid helpers ───────────────────────────────────────────────────────

// Query a data-testid element (throws if not found in dev, returns null in prod).
function byTestId(id, root) {
    return (root || document).querySelector(`[data-testid="${id}"]`);
}

// Set text content of an element by ID (no-op if element doesn't exist).
function setText(id, val) { const el = document.getElementById(id); if (el) el.textContent = val; }

// ── Router ────────────────────────────────────────────────────────────────────
// Thin hash-routing wrapper. Does not replace switchTab — wraps it.
// Usage:
//   router.navigate('catalog')           → sets location.hash = '#catalog'
//   router.navigate('catalog', {id: 5})  → sets hash = '#catalog/id=5'
//   router.current()                     → { tab: 'catalog', params: {id:'5'} }
//   router.listen(fn)                    → calls fn({tab, params}) on hashchange + immediately

const router = {
    navigate(tab, params) {
        let hash = '#' + tab;
        if (params) {
            const qs = Object.entries(params).map(([k,v]) => k + '=' + encodeURIComponent(v)).join('&');
            if (qs) hash += '/' + qs;
        }
        location.hash = hash;
    },

    current() {
        const h = location.hash.slice(1); // strip #
        if (!h) return { tab: '', params: {} };
        const [tab, rest] = h.split('/', 2);
        const params = {};
        if (rest) rest.split('&').forEach(pair => {
            const [k, v] = pair.split('=', 2);
            if (k) params[k] = v ? decodeURIComponent(v) : '';
        });
        return { tab, params };
    },

    listen(fn) {
        const handler = () => fn(router.current());
        window.addEventListener('hashchange', handler);
        handler(); // call immediately with current state
        return () => window.removeEventListener('hashchange', handler);
    }
};

// ── Focus trap ────────────────────────────────────────────────────────────────
// trapFocus(el) — traps Tab/Shift-Tab within el, closes on Escape.
// releaseFocus(el) — removes trap, restores focus to previously active element.

const _focusTraps = new WeakMap();

function trapFocus(el, onClose) {
    const prev = document.activeElement;
    const focusable = el.querySelectorAll(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
    );
    const first = focusable[0];
    const last = focusable[focusable.length - 1];

    function handler(e) {
        if (e.key === 'Escape') { if (onClose) onClose(); return; }
        if (e.key !== 'Tab') return;
        if (focusable.length === 0) { e.preventDefault(); return; }
        if (e.shiftKey) {
            if (document.activeElement === first) { e.preventDefault(); last.focus(); }
        } else {
            if (document.activeElement === last) { e.preventDefault(); first.focus(); }
        }
    }

    el.addEventListener('keydown', handler);
    _focusTraps.set(el, { handler, prev });
    if (first) first.focus();
}

function releaseFocus(el) {
    const trap = _focusTraps.get(el);
    if (!trap) return;
    el.removeEventListener('keydown', trap.handler);
    _focusTraps.delete(el);
    if (trap.prev && typeof trap.prev.focus === 'function') trap.prev.focus();
}

// ── Exports (assigned to window for plain-script use) ─────────────────────────

window.PeliculaFW = { createStore, component, mount, unmount, html, raw, initStore, byTestId, setText, esc: _escapeHtml, router, trapFocus, releaseFocus };

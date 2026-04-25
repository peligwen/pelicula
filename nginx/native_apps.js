// native_apps.js — wires the "Native apps" popover next to the Watch button.
// Fetches /api/pelicula/jellyfin/info (public, non-secret) and shows a copyable
// LAN URL so users can paste it into the Jellyfin app on iOS / Apple TV /
// Android TV / Roku.
import { get } from './api.js';

(function () {
    const trigger = document.getElementById('watch-apps-btn');
    const popover = document.getElementById('watch-apps-popover');
    const urlEl = document.getElementById('watch-apps-url');
    const copyBtn = document.getElementById('watch-apps-copy');
    const emptyEl = document.getElementById('watch-apps-empty');
    const group = document.getElementById('watch-group');
    if (!trigger || !popover || !urlEl || !copyBtn) return;

    let loaded = false;
    let lanUrl = '';

    async function loadInfo() {
        if (loaded) return;
        loaded = true;
        try {
            const data = await get('/api/pelicula/jellyfin/info');
            lanUrl = (data && data.lan_url) || '';
        } catch (_) {
            lanUrl = '';
        }
        if (lanUrl) {
            urlEl.textContent = lanUrl;
            urlEl.classList.remove('hidden');
            copyBtn.classList.remove('hidden');
            if (emptyEl) emptyEl.classList.add('hidden');
        } else {
            urlEl.classList.add('hidden');
            copyBtn.classList.add('hidden');
            if (emptyEl) emptyEl.classList.remove('hidden');
        }
    }

    function open() {
        loadInfo();
        popover.classList.remove('hidden');
        trigger.setAttribute('aria-expanded', 'true');
    }
    function close() {
        popover.classList.add('hidden');
        trigger.setAttribute('aria-expanded', 'false');
    }
    function toggle() {
        if (popover.classList.contains('hidden')) {
            open();
        } else {
            close();
        }
    }

    trigger.addEventListener('click', function (e) {
        e.preventDefault();
        e.stopPropagation();
        toggle();
    });

    // Click outside closes the popover.
    document.addEventListener('click', function (e) {
        if (popover.classList.contains('hidden')) return;
        if (group && group.contains(e.target)) return;
        close();
    });

    // Escape closes the popover.
    document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && !popover.classList.contains('hidden')) {
            close();
            trigger.focus();
        }
    });

    copyBtn.addEventListener('click', async function () {
        if (!lanUrl) return;
        try {
            await navigator.clipboard.writeText(lanUrl);
            const orig = copyBtn.textContent;
            copyBtn.textContent = 'Copied!';
            setTimeout(function () { copyBtn.textContent = orig; }, 1500);
        } catch (_) {
            // Clipboard API blocked — fall back to text selection.
            const range = document.createRange();
            range.selectNodeContents(urlEl);
            const sel = window.getSelection();
            sel.removeAllRanges();
            sel.addRange(range);
        }
    });
})();

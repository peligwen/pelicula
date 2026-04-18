// nginx/services.js
// Services sidebar component — owns: service health checks, 30s auto-refresh, VPN telemetry,
// log viewer modal, stack actions, host stats, and the panel-alert signal.
'use strict';

import { component, html, raw, toast, createPoller, setText } from '/framework.js';
import { get, post } from '/api.js';

// ── Module-level state ────────────────────────────────────────────────────
let _panelVPNDegraded = false;
let _vpnBannerDismissed = false;
let _logCurrentSvc = '';

const SVC_INTERVAL = 30;
let svcPoller = null;

// ── Panel alert signal ────────────────────────────────────────────────────

function updatePanelAlert() {
    const pips = document.querySelectorAll('#svc-sidebar-list .svc-pip');
    let unhealthyCount = 0;
    pips.forEach(p => {
        if (p.classList.contains('down') || p.classList.contains('unknown')) unhealthyCount++;
    });
    const unhealthy = unhealthyCount > 0;
    document.body.classList.toggle('panel-alert', unhealthy);
}

// ── Services ──────────────────────────────────────────────────────────────

function updateServicesFromData(data) {
    const warn = document.getElementById('search-warning');
    const svcMap = data.services || {};
    Object.keys(svcMap).forEach(name => {
        const pip = document.getElementById('svc-pip-' + name);
        if (!pip) return;
        const up = svcMap[name] === 'up';
        pip.className = 'svc-pip ' + (up ? 'up' : 'down');
        const row = pip.closest('.svc-row');
        if (row) { row.classList.remove('svc-up', 'svc-down', 'svc-unknown'); row.classList.add(up ? 'svc-up' : 'svc-down'); }
    });
    updateSvcTotals();
    const radarrUp = svcMap['radarr'] === 'up';
    const sonarrUp = svcMap['sonarr'] === 'up';
    const sinput = document.getElementById('search-input');
    if (!radarrUp && !sonarrUp) {
        if (sinput) { sinput.disabled = true; sinput.placeholder = 'Search unavailable'; }
        if (warn) { warn.textContent = 'Radarr and Sonarr are both down \u2014 search is disabled'; warn.className = 'search-warning err'; }
    } else if (!radarrUp || !sonarrUp) {
        if (sinput) { sinput.disabled = false; sinput.placeholder = 'Search for a title...'; }
        const down = !radarrUp ? 'Radarr (movies)' : 'Sonarr (TV shows)';
        if (warn) { warn.textContent = down + ' is down \u2014 some results may be missing'; warn.className = 'search-warning warn'; }
    } else {
        if (sinput) { sinput.disabled = false; sinput.placeholder = 'Search for a title...'; }
        if (warn) warn.className = 'search-warning';
    }
}

async function checkServices() {
    try {
        const data = await get('/api/pelicula/status');
        if (data === null) return;
        updateServicesFromData(data);
        return data;
    } catch (e) {
        console.warn('[pelicula] status check error:', e);
        const warn = document.getElementById('search-warning');
        document.querySelectorAll('.svc-pip').forEach(pip => {
            pip.className = 'svc-pip unknown';
            const row = pip.closest('.svc-row');
            if (row) { row.classList.remove('svc-up', 'svc-down'); row.classList.add('svc-unknown'); }
        });
        updateSvcTotals();
        const sinput = document.getElementById('search-input');
        if (sinput) { sinput.disabled = true; sinput.placeholder = 'Search unavailable'; }
        if (warn) { warn.textContent = 'Cannot reach services \u2014 search is disabled'; warn.className = 'search-warning err'; }
    }
}

function manualRefreshServices() {
    if (svcPoller) svcPoller.refresh(); else checkServices().then(updateSvcTotals);
}

function updateSvcTotals() {
    const pips = document.querySelectorAll('#svc-sidebar-list .svc-pip');
    let up = 0, down = 0;
    pips.forEach(p => {
        if (p.classList.contains('up')) up++;
        else if (p.classList.contains('down')) down++;
    });
    const el = document.getElementById('svc-totals');
    if (!el) return;
    if (down === 0 && up > 0) {
        el.textContent = up + '\u202f\u2713';
        el.style.color = '#7dda93';
    } else if (down > 0) {
        el.textContent = up + '\u2191\u00b7' + down + '\u2193';
        el.style.color = '#f87171';
    } else {
        el.textContent = '';
        el.style.color = '';
    }
    updatePanelAlert();
}

// ── Stack actions ──────────────────────────────────────────────────────────

function toggleStackMenu() {
    const menu = document.getElementById('svc-stack-menu');
    if (menu) menu.classList.toggle('hidden');
}

document.addEventListener('click', (e) => {
    const menu = document.getElementById('svc-stack-menu');
    const wrap = document.querySelector('.svc-stack-menu-wrap');
    if (menu && wrap && !wrap.contains(e.target)) {
        menu.classList.add('hidden');
    }
});

async function stackRestart() {
    const btn = document.getElementById('svc-menu-btn');
    if (!confirm('Restart all stack services? The dashboard will reconnect automatically.')) return;
    toggleStackMenu();
    if (btn) btn.disabled = true;
    try {
        const data = await post('/api/pelicula/admin/stack/restart', {});
        toast('Stack restarting\u2026');
        setTimeout(() => checkServices().then(updateSvcTotals), 5000);
    } catch (e) {
        // pelicula-api restarted — response may have been lost. That's fine.
        toast('Stack restarting\u2026');
        setTimeout(() => checkServices().then(updateSvcTotals), 5000);
    } finally {
        if (btn) btn.disabled = false;
    }
}

// ── Log viewer modal ──────────────────────────────────────────────────────

function showServiceLogs(e, svc) {
    e.stopPropagation();
    e.preventDefault();
    _logCurrentSvc = svc;
    const modal = document.getElementById('log-modal');
    const title = document.getElementById('log-modal-title');
    const pre = document.getElementById('log-modal-pre');
    if (!modal) return;
    title.textContent = svc + ' logs';
    pre.textContent = 'Loading\u2026';
    modal.classList.remove('hidden');
    fetchServiceLogs(svc);
}

function closeLogModal() {
    const modal = document.getElementById('log-modal');
    if (modal) modal.classList.add('hidden');
    _logCurrentSvc = '';
}

function refreshServiceLogs() {
    if (_logCurrentSvc) fetchServiceLogs(_logCurrentSvc);
}

function copyServiceLogs() {
    const pre = document.getElementById('log-modal-pre');
    const btn = document.getElementById('log-copy-btn');
    if (!pre || !btn) return;
    const text = pre.textContent || '';
    const flash = () => {
        const prev = btn.textContent;
        btn.textContent = 'Copied!';
        setTimeout(() => { btn.textContent = prev; }, 1500);
    };
    if (navigator.clipboard) {
        navigator.clipboard.writeText(text).then(flash).catch(() => {
            const r = document.createRange();
            r.selectNodeContents(pre);
            const sel = window.getSelection();
            sel.removeAllRanges(); sel.addRange(r);
        });
    }
}

async function fetchServiceLogs(svc) {
    const pre = document.getElementById('log-modal-pre');
    const btn = document.getElementById('log-refresh-btn');
    if (btn) btn.disabled = true;
    try {
        // Log fetch returns plain text, not JSON — use raw fetch.
        const res = await fetch('/api/pelicula/admin/logs?svc=' + encodeURIComponent(svc) + '&tail=200', { credentials: 'same-origin' });
        if (!res.ok) {
            const d = await res.json().catch(() => ({}));
            pre.textContent = 'Error: ' + (d.error || res.status);
            return;
        }
        const text = await res.text();
        pre.textContent = text || '(no output)';
        pre.scrollTop = pre.scrollHeight;
    } catch (e) {
        pre.textContent = 'Network error: ' + e.message;
    } finally {
        if (btn) btn.disabled = false;
    }
}

// ── VPN Telemetry ─────────────────────────────────────────────────────────

async function checkVPN() {
    // fetchServiceLogs returns plain text so we can't use api.get for that endpoint,
    // but VPN endpoints return JSON and work with api.get.
    // vpnPublicIP and portforward return JSON; health returns JSON.
    try {
        const [ipResult, portResult, healthResult] = await Promise.allSettled([
            get('/api/vpn/v1/publicip/ip'),
            get('/api/vpn/v1/portforward'),
            get('/api/pelicula/health')
        ]);

        const ipData = ipResult.status === 'fulfilled' ? ipResult.value : null;
        if (ipData !== null && ipData) {
            setText('s-region', ipData.country || '\u2014');
        } else if (ipData === null && ipResult.status === 'rejected') {
            throw new Error('VPN timeout');
        }

        let portDegraded = false;
        if (healthResult.status === 'fulfilled' && healthResult.value) {
            const hd = healthResult.value;
            portDegraded = hd.vpn && hd.vpn.port_status === 'degraded';
        }

        const portEl = document.getElementById('s-port');
        if (portDegraded) {
            if (portEl) {
                portEl.textContent = 'No forwarding';
                portEl.classList.add('vpn-v-error');
            }
        } else {
            if (portEl) portEl.classList.remove('vpn-v-error');
            const portData = portResult.status === 'fulfilled' ? portResult.value : null;
            if (portData !== null && portData) {
                setText('s-port', portData.port || '\u2014');
            }
        }

        updateVPNPortBanner(portDegraded);
    } catch (e) {
        console.warn('[pelicula] VPN telemetry error:', e);
        setText('s-region', '\u2014');
        setText('s-port', '\u2014');
        const portEl = document.getElementById('s-port');
        if (portEl) portEl.classList.remove('vpn-v-error');
    }
}

function updateVPNPortBanner(degraded) {
    _panelVPNDegraded = degraded;
    updatePanelAlert();
    const bannerId = 'vpn-port-warn-banner';
    let banner = document.getElementById(bannerId);
    if (!degraded) {
        _vpnBannerDismissed = false;
        if (banner) banner.remove();
        return;
    }
    if (_vpnBannerDismissed || banner) return;

    banner = document.createElement('div');
    banner.id = bannerId;
    banner.className = 'vpn-port-warn-banner';

    const msg = document.createTextNode(
        'Port forwarding is unavailable \u2014 download speeds will be limited. '
    );
    banner.appendChild(msg);

    const restartBtn = document.createElement('button');
    restartBtn.textContent = 'Restart VPN';
    restartBtn.addEventListener('click', function() { restartVPN(restartBtn); });
    banner.appendChild(restartBtn);

    const dismissBtn = document.createElement('button');
    dismissBtn.className = 'banner-dismiss';
    dismissBtn.textContent = '\u00d7';
    dismissBtn.addEventListener('click', function() {
        _vpnBannerDismissed = true;
        banner.remove();
    });
    banner.appendChild(dismissBtn);

    (document.querySelector('.pane-main') || document.body).prepend(banner);
}

// VPN restart takes up to 35s — use raw fetch with long timeout.
async function restartVPN(btn) {
    btn.disabled = true;
    btn.textContent = 'Restarting\u2026';
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), 35000);
    try {
        const res = await fetch('/api/pelicula/admin/vpn/restart', {
            method: 'POST', credentials: 'same-origin', signal: ctrl.signal
        });
        if (res && res.ok) {
            btn.textContent = 'Restarted';
        } else {
            btn.textContent = 'Failed';
            btn.disabled = false;
        }
    } catch (e) {
        btn.textContent = 'Failed';
        btn.disabled = false;
    } finally {
        clearTimeout(t);
    }
}

// ── Speed test ────────────────────────────────────────────────────────────

// Speed test takes up to 35s — use raw fetch with long timeout.
async function runSpeedTest() {
    const btn = document.getElementById('btn-speedtest');
    const resultEl = document.getElementById('s-speedtest-result');
    if (btn) { btn.disabled = true; btn.textContent = 'Testing\u2026'; }
    if (resultEl) resultEl.textContent = '';
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), 35000);
    try {
        const res = await fetch('/api/pelicula/speedtest', {
            method: 'POST', credentials: 'same-origin', signal: ctrl.signal
        });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        if (data.error) {
            if (resultEl) { resultEl.textContent = 'Error: ' + data.error; resultEl.className = 'vpn-speedtest-result vpn-st-red'; }
        } else {
            const mbps = data.download_mbps || 0;
            const color = mbps >= 25 ? 'vpn-st-green' : mbps >= 10 ? 'vpn-st-yellow' : 'vpn-st-red';
            if (resultEl) {
                resultEl.textContent = mbps.toFixed(1) + ' Mbps';
                resultEl.className = 'vpn-speedtest-result ' + color;
                resultEl.title = 'Tested ' + new Date(data.timestamp * 1000).toLocaleTimeString();
            }
        }
    } catch (e) {
        if (resultEl) { resultEl.textContent = 'Failed'; resultEl.className = 'vpn-speedtest-result vpn-st-red'; }
    } finally {
        clearTimeout(t);
        if (btn) { btn.disabled = false; btn.textContent = 'Test VPN Speed'; }
    }
}

// ── Host stats ────────────────────────────────────────────────────────────

function fmtUptime(secs) {
    const s = Math.floor(secs);
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    return d > 0 ? (d + 'd ' + h + 'h') : (h + 'h ' + m + 'm');
}

async function checkHost() {
    try {
        const d = await get('/api/pelicula/host');
        if (d === null) return;
        setText('s-uptime', fmtUptime(d.uptime_seconds || 0));
        if (d.disk && d.disk.total > 0) {
            setText('s-space', formatSize(d.disk.free) + ' free / ' + formatSize(d.disk.total));
            const bar = document.getElementById('s-space-bar');
            if (bar) bar.style.width = Math.round(d.disk.used_pct) + '%';
        }
        if (d.library) {
            const parts = [];
            if (d.library.movies) parts.push(d.library.movies + ' movies');
            if (d.library.series) parts.push(d.library.series + ' series');
            setText('s-library', parts.join(' \u00b7 ') || '\u2014');
        }
    } catch (e) { console.warn('[pelicula] host error:', e); }
}

function updateTimestamp() { document.getElementById('footer-time').textContent = new Date().toLocaleTimeString(); }

// ── Component registration ────────────────────────────────────────────────

component('services', function (el, storeProxy) {
    function init() {
        const logModal = document.getElementById('log-modal');
        if (logModal) {
            logModal.addEventListener('click', (e) => {
                if (e.target === logModal) closeLogModal();
            });
        }
    }

    return {
        render: function() {},
        loadOnce: function() {
            init();
            svcPoller = createPoller(
                function() { checkServices().then(updateSvcTotals); },
                SVC_INTERVAL
            );
            svcPoller.start();
            window.svcPoller = svcPoller;
        },
    };
});

// ── Static button listeners ───────────────────────────────────────────────
document.getElementById('svc-refresh-btn').addEventListener('click', manualRefreshServices);
document.getElementById('svc-menu-btn').addEventListener('click', toggleStackMenu);
document.getElementById('svc-stack-restart-btn').addEventListener('click', stackRestart);
document.getElementById('log-copy-btn').addEventListener('click', copyServiceLogs);
document.getElementById('log-refresh-btn').addEventListener('click', refreshServiceLogs);
document.getElementById('log-close-btn').addEventListener('click', closeLogModal);
document.getElementById('btn-speedtest').addEventListener('click', runSpeedTest);

// Service log button delegation — data-svc set in HTML
document.getElementById('svc-sidebar-list').addEventListener('click', e => {
    const btn = e.target.closest('.svc-row-log[data-svc]');
    if (btn) showServiceLogs(e, btn.dataset.svc);
});

// ── Window exports ────────────────────────────────────────────────────────
window.checkServices          = checkServices;
window.updateServicesFromData = updateServicesFromData;
window.checkVPN               = checkVPN;
window.checkHost              = checkHost;
window.updateTimestamp        = updateTimestamp;

function formatSize(b) { return window.formatSize ? window.formatSize(b) : ''; }

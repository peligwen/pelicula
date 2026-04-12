// nginx/services.js
// Services sidebar component — registered with PeliculaFW; mounted by dashboard.js.
// Owns: service health checks, 30s auto-refresh, VPN telemetry, log viewer modal,
//       stack actions, host stats, and the panel-alert signal.
// Depends on: framework.js (PeliculaFW), dashboard.js (tfetch, html, raw).

'use strict';

(function () {
    const { component, html, raw, toast, createPoller } = PeliculaFW;

    // ── Module-level state ────────────────────────────────────────────────────
    let _panelVPNDegraded = false;
    let _vpnBannerDismissed = false;
    let _logCurrentSvc = '';

    const SVC_INTERVAL = 30; // seconds
    // Poller is initialised lazily in loadOnce (needs checkServices defined first)
    let svcPoller = null;

    // ── Panel alert signal ────────────────────────────────────────────────────
    // Derives body.panel-alert from service health + VPN degraded flag.
    // A pip is "unhealthy" when it's explicitly .down OR .unknown — the latter
    // covers the case where /api/pelicula/status itself is unreachable, which
    // is the single most important failure mode the user wants surfaced.
    // Called by updateSvcTotals() and updateVPNPortBanner() — no new polling.

    function updatePanelAlert() {
        const pips = document.querySelectorAll('#svc-sidebar-list .svc-pip');
        let unhealthyCount = 0;
        pips.forEach(p => {
            if (p.classList.contains('down') || p.classList.contains('unknown')) unhealthyCount++;
        });
        const unhealthy = unhealthyCount > 0 || _panelVPNDegraded;
        document.body.classList.toggle('panel-alert', unhealthy);
    }

    // ── Services ──────────────────────────────────────────────────────────────

    async function checkServices() {
        const warn = document.getElementById('search-warning');
        try {
            const res = await tfetch('/api/pelicula/status');
            if (!res.ok) throw new Error();
            const data = await res.json();
            const svcMap = data.services || {};
            // Update sidebar pips
            Object.keys(svcMap).forEach(name => {
                const pip = document.getElementById('svc-pip-' + name);
                if (!pip) return;
                const up = svcMap[name] === 'up';
                pip.className = 'svc-pip ' + (up ? 'up' : 'down');
                const row = pip.closest('.svc-row');
                if (row) { row.classList.remove('svc-up', 'svc-down', 'svc-unknown'); row.classList.add(up ? 'svc-up' : 'svc-down'); }
            });
            updateSvcTotals();
            // Search depends on Radarr + Sonarr
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
        } catch (e) {
            console.warn('[pelicula] status check error:', e);
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

    // ── Services auto-refresh + totals ────────────────────────────────────────

    // Auto-refresh timer managed by framework's createPoller (see loadOnce below)

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
            const res = await fetch('/api/pelicula/admin/stack/restart', { method: 'POST' });
            const data = await res.json().catch(() => ({}));
            if (!res.ok) { toast(data.error || 'Restart failed', { error: true }); return; }
            toast('Stack restarting\u2026');
            setTimeout(() => checkServices().then(updateSvcTotals), 5000);
        } catch (e) {
            // pelicula-api restarted — response was lost. That's fine.
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
            const res = await fetch('/api/pelicula/admin/logs?svc=' + encodeURIComponent(svc) + '&tail=200');
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
        try {
            const [ipResult, portResult, healthResult] = await Promise.allSettled([
                tfetch('/api/vpn/v1/publicip/ip'),
                tfetch('/api/vpn/v1/portforward'),
                tfetch('/api/pelicula/health')
            ]);

            // Region
            const ipRes = ipResult.status === 'fulfilled' ? ipResult.value : null;
            if (ipRes && ipRes.ok) {
                const data = await ipRes.json();
                setText('s-region', data.country || '\u2014');
            } else if (!ipRes) {
                throw new Error('VPN timeout');
            }

            // Port forwarding status from middleware watchdog
            let portDegraded = false;
            if (healthResult.status === 'fulfilled' && healthResult.value.ok) {
                const hd = await healthResult.value.json();
                portDegraded = hd.vpn && hd.vpn.port_status === 'degraded';
            }

            // Port display — show error text when degraded, numeric port otherwise
            const portEl = document.getElementById('s-port');
            if (portDegraded) {
                if (portEl) {
                    portEl.textContent = 'No forwarding';
                    portEl.classList.add('vpn-v-error');
                }
            } else {
                if (portEl) portEl.classList.remove('vpn-v-error');
                const portRes = portResult.status === 'fulfilled' ? portResult.value : null;
                if (portRes && portRes.ok) {
                    const pd = await portRes.json();
                    setText('s-port', pd.port || '\u2014');
                }
            }

            updateVPNPortBanner(portDegraded);
        } catch (e) {
            // Note: we deliberately do NOT reset _panelVPNDegraded here.
            // A telemetry error is itself a reason to keep any existing alert lit.
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
            _vpnBannerDismissed = false; // reset so banner re-shows if port degrades again later
            if (banner) banner.remove();
            return;
        }
        if (_vpnBannerDismissed || banner) return; // already showing or dismissed this session

        // Build banner using safe DOM methods (no innerHTML).
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

        const pipelineSection = document.getElementById('pipeline-section');
        if (pipelineSection) {
            pipelineSection.insertAdjacentElement('beforebegin', banner);
        } else {
            (document.querySelector('.main-content') || document.body).prepend(banner);
        }
    }

    async function restartVPN(btn) {
        btn.disabled = true;
        btn.textContent = 'Restarting\u2026';
        try {
            const res = await tfetch('/api/pelicula/admin/vpn/restart', { method: 'POST' }, 35000);
            if (res && res.ok) {
                btn.textContent = 'Restarted';
            } else {
                btn.textContent = 'Failed';
                btn.disabled = false;
            }
        } catch (e) {
            btn.textContent = 'Failed';
            btn.disabled = false;
        }
    }

    // ── Speed test ────────────────────────────────────────────────────────────

    async function runSpeedTest() {
        const btn = document.getElementById('btn-speedtest');
        const resultEl = document.getElementById('s-speedtest-result');
        if (btn) { btn.disabled = true; btn.textContent = 'Testing\u2026'; }
        if (resultEl) resultEl.textContent = '';
        try {
            const res = await tfetch('/api/pelicula/speedtest', {method: 'POST'}, 35000);
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
            const res = await tfetch('/api/pelicula/host');
            if (!res.ok) throw new Error();
            const d = await res.json();
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
            // Close log modal on overlay click
            const logModal = document.getElementById('log-modal');
            if (logModal) {
                logModal.addEventListener('click', (e) => {
                    if (e.target === logModal) closeLogModal();
                });
            }
        }

        return {
            render: function() {},  // no template rendering — operates on existing DOM
            loadOnce: function() {
                init();
                // Start the 30-second auto-refresh timer via framework poller.
                // The first actual check fires from refresh() in dashboard.js.
                svcPoller = createPoller(
                    function() { checkServices().then(updateSvcTotals); },
                    SVC_INTERVAL,
                    'svc-refresh-status'
                );
                svcPoller.start();
            },
        };
    });

    // ── Window exports ────────────────────────────────────────────────────────
    window.checkServices         = checkServices;
    window.checkVPN              = checkVPN;
    window.checkHost             = checkHost;
    window.updateTimestamp       = updateTimestamp;
    window.manualRefreshServices = manualRefreshServices;
    window.toggleStackMenu       = toggleStackMenu;
    window.stackRestart          = stackRestart;
    window.showServiceLogs       = showServiceLogs;
    window.closeLogModal         = closeLogModal;
    window.refreshServiceLogs    = refreshServiceLogs;
    window.copyServiceLogs       = copyServiceLogs;
    window.runSpeedTest          = runSpeedTest;
}());

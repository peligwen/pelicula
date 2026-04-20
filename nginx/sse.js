// nginx/sse.js
// SSE client — connects to /api/pelicula/sse and dispatches server-pushed events
// to existing render functions on window. When SSE is active, polling stops.
// When SSE drops (after 3 retries), polling resumes and SSE auto-reconnects.
'use strict';

if (typeof EventSource === 'undefined') {
    window.connectSSE    = function() {};
    window.disconnectSSE = function() {};
    window.sseIsActive   = function() { return false; };
} else {
    let source = null;
    let retryCount = 0;
    let sseActive = false;
    let _started = false;

    function connect() {
        if (source) source.close();
        source = new EventSource('/api/pelicula/sse');

        source.onopen = function() {
            retryCount = 0;
            sseActive = true;
            disablePollers();
            if (window.markRefreshed) window.markRefreshed();
        };

        source.onerror = function() {
            retryCount++;
            sseActive = false;
            if (retryCount === 3) {
                enablePollers();
                // EventSource auto-reconnects; don't close it
            }
        };

        // services event: same shape as /api/pelicula/status
        source.addEventListener('services', function(e) {
            try {
                const data = JSON.parse(e.data);
                if (window.updateServicesFromData) window.updateServicesFromData(data);
                if (window.markRefreshed) window.markRefreshed();
            } catch(err) { console.warn('[sse] services parse error', err); }
        });

        // downloads event: trigger a targeted re-fetch
        source.addEventListener('downloads', function() {
            if (window.checkDownloads) window.checkDownloads();
            if (window.markRefreshed) window.markRefreshed();
        });

        // notifications event: array shape matches renderNotifications
        source.addEventListener('notifications', function(e) {
            try {
                const data = JSON.parse(e.data);
                if (window.renderNotifications) window.renderNotifications(data);
                if (window.renderActivity) window.renderActivity(data);
                if (window.markRefreshed) window.markRefreshed();
            } catch(err) { console.warn('[sse] notifications parse error', err); }
        });

        // storage event: trigger targeted re-fetch
        source.addEventListener('storage', function() {
            if (window.checkStorage) window.checkStorage();
            if (window.markRefreshed) window.markRefreshed();
        });

        // logs event: {entries: [{service, line, ts},...]}
        source.addEventListener('logs', function(e) {
            try {
                const data = JSON.parse(e.data);
                if (window.renderLogsFromSSE) window.renderLogsFromSSE(data);
                if (window.markRefreshed) window.markRefreshed();
            } catch(err) { console.warn('[sse] logs parse error', err); }
        });
    }

    function disablePollers() {
        if (window.svcPoller && window.svcPoller.stop) window.svcPoller.stop();
        if (window._refreshInterval) {
            clearInterval(window._refreshInterval);
            window._refreshInterval = null;
        }
    }

    function enablePollers() {
        if (window.svcPoller && window.svcPoller.start) window.svcPoller.start();
        if (!window._refreshInterval && window.refresh) {
            window._refreshInterval = setInterval(window.refresh, 15000);
        }
    }

    window.connectSSE = function() {
        if (_started) return;
        _started = true;
        connect();
    };
    window.disconnectSSE = function() {
        _started = false;
        if (source) { source.close(); source = null; }
        sseActive = false;
        enablePollers();
    };
    window.sseIsActive = function() { return sseActive; };
}

// nginx/sse.js
// SSE client — connects to /api/pelicula/sse and dispatches server-pushed events
// to existing render functions on window. When SSE is active, polling stops.
// When SSE drops (after 3 retries), polling resumes and SSE auto-reconnects.
'use strict';
(function() {
    if (typeof EventSource === 'undefined') return; // unsupported browser guard

    var source = null;
    var retryCount = 0;
    var sseActive = false;
    var _started = false;

    function connect() {
        if (source) source.close();
        source = new EventSource('/api/pelicula/sse');

        source.onopen = function() {
            retryCount = 0;
            sseActive = true;
            disablePollers();
        };

        source.onerror = function() {
            retryCount++;
            sseActive = false;
            if (retryCount === 3) {
                enablePollers();
                // EventSource auto-reconnects; don't close it
            }
        };

        // pipeline event: same shape as /api/pelicula/pipeline
        // renderPipeline(data) is exported by pipeline.js
        source.addEventListener('pipeline', function(e) {
            try {
                var data = JSON.parse(e.data);
                if (window.renderPipeline) window.renderPipeline(data);
            } catch(err) { console.warn('[sse] pipeline parse error', err); }
        });

        // services event: same shape as /api/pelicula/status
        // updateServicesFromData(data) is exported by services.js
        source.addEventListener('services', function(e) {
            try {
                var data = JSON.parse(e.data);
                if (window.updateServicesFromData) window.updateServicesFromData(data);
            } catch(err) { console.warn('[sse] services parse error', err); }
        });

        // downloads event: shape may differ from /api/pelicula/downloads
        // Trigger a targeted re-fetch rather than trying to render raw SSE data
        source.addEventListener('downloads', function() {
            if (window.checkDownloads) window.checkDownloads();
        });

        // notifications event: array shape matches renderNotifications
        source.addEventListener('notifications', function(e) {
            try {
                var data = JSON.parse(e.data);
                if (window.renderNotifications) window.renderNotifications(data);
                if (window.renderActivity) window.renderActivity(data);
            } catch(err) { console.warn('[sse] notifications parse error', err); }
        });

        // storage event: same shape as /api/pelicula/storage (procula proxy)
        // Trigger targeted re-fetch for simplicity (multiple render functions involved)
        source.addEventListener('storage', function() {
            if (window.checkStorage) window.checkStorage();
        });

        // logs event: {entries: [{service, line, ts},...]} — interleaved across services,
        // newest first. renderLogsFromSSE is exported by logs.js.
        source.addEventListener('logs', function(e) {
            try {
                var data = JSON.parse(e.data);
                if (window.renderLogsFromSSE) window.renderLogsFromSSE(data);
            } catch(err) { console.warn('[sse] logs parse error', err); }
        });
    }

    function disablePollers() {
        if (window.plPoller && window.plPoller.stop) window.plPoller.stop();
        if (window.svcPoller && window.svcPoller.stop) window.svcPoller.stop();
        if (window._refreshInterval) {
            clearInterval(window._refreshInterval);
            window._refreshInterval = null;
        }
    }

    function enablePollers() {
        if (window.plPoller && window.plPoller.start) window.plPoller.start();
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
}());

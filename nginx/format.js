// nginx/format.js
// Shared format helpers — exposed on window so all modules can call them.
// Loaded as a plain (non-module) script before dashboard.js so the functions
// are defined synchronously before any ES module evaluates.
'use strict';

window.formatSpeed = function formatSpeed(bps) {
    if (bps > 1048576) return (bps / 1048576).toFixed(1) + ' MB/s';
    if (bps > 1024) return (bps / 1024).toFixed(0) + ' KB/s';
    if (bps > 0) return bps + ' B/s';
    return 'idle';
};

window.formatSize = function formatSize(b) {
    if (!b) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, n = b;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return n.toFixed(1) + ' ' + u[i];
};

window.formatETA = function formatETA(s) {
    if (s > 86400) return Math.floor(s / 86400) + 'd';
    if (s > 3600) return Math.floor(s / 3600) + 'h ' + Math.floor((s % 3600) / 60) + 'm';
    if (s > 60) return Math.floor(s / 60) + 'm';
    return s + 's';
};

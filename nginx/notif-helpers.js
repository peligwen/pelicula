// nginx/notif-helpers.js
// Shared notification helper functions — exposed as window globals so both
// notifications.js and activity.js (ES modules) can consume them without a
// circular import.  Loaded as a classic <script> before any module in index.html.
'use strict';

window.notifIcon = function notifIcon(type) {
    if (type === 'content_ready') return '&#10003;';
    if (type === 'storage_warning' || type === 'storage_critical') return '&#9632;';
    return '&#9888;';
};

window.notifClass = function notifClass(type) {
    if (type === 'content_ready') return 'notif-ready';
    if (type === 'storage_warning' || type === 'storage_critical') return 'notif-storage';
    return 'notif-failed';
};

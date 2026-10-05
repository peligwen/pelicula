/* Shared helpers for the dashboard and the register page.
 * No framework, no inline handlers (the CSP forbids inline script). */
(function (root) {
  'use strict';

  var ESC = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };

  // The one HTML escaper. Every API-provided string that reaches innerHTML goes through it.
  function esc(v) {
    return String(v == null ? '' : v).replace(/[&<>"']/g, function (c) { return ESC[c]; });
  }

  function ApiError(status, message, body) {
    this.name = 'ApiError';
    this.status = status;
    this.message = message;
    this.body = body || null;
  }
  ApiError.prototype = Object.create(Error.prototype);
  ApiError.prototype.constructor = ApiError;

  var FRIENDLY = {
    429: 'Too many attempts. Wait a minute and try again.',
    502: 'The server is not reachable right now.',
    503: 'The service is unavailable right now.',
    504: 'The server took too long to answer.'
  };

  /* api(method, path, body?, opts?) -> parsed JSON (null for 204).
   * Rejects with ApiError. A 401 also calls api.onUnauthorized (the login
   * overlay) unless opts.noAuthHook is set (used by the login form itself). */
  function api(method, path, body, opts) {
    var init = { method: method, credentials: 'same-origin', headers: { Accept: 'application/json' } };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    return fetch(path, init).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        if (text) {
          try { data = JSON.parse(text); } catch (e) { data = null; }
        }
        if (res.ok) return data;
        var msg = (data && data.error) || FRIENDLY[res.status] || ('Request failed (' + res.status + ')');
        var err = new ApiError(res.status, msg, data);
        if (res.status === 401 && api.onUnauthorized && !(opts && opts.noAuthHook)) api.onUnauthorized(err);
        throw err;
      });
    }, function () {
      throw new ApiError(0, 'Cannot reach the server.');
    });
  }
  api.onUnauthorized = null;

  root.Pelicula = { esc: esc, api: api, ApiError: ApiError };
})(window);

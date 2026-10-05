/* Invite redemption page: GET /api/register/{code}, then POST /api/register. */
(function () {
  'use strict';

  const { api } = window.Pelicula;
  const $ = (sel) => document.querySelector(sel);
  const code = new URLSearchParams(location.search).get('code') || '';
  const NAME_RE = /^[A-Za-z0-9._-]{3,32}$/;

  function invalid(msg) {
    $('#register-loading').hidden = true;
    $('#register-form').hidden = true;
    $('#register-invalid-msg').textContent = msg;
    $('#register-invalid').hidden = false;
  }

  function formError(msg) {
    const el = $('#register-error');
    el.textContent = msg || '';
    el.hidden = !msg;
  }

  async function init() {
    if (!code) { invalid('This invite link is missing its code.'); return; }
    try {
      const d = await api('GET', '/api/register/' + encodeURIComponent(code));
      if (!d || !d.valid) { invalid('This invite is invalid, expired or already used.'); return; }
      $('#register-role').textContent = d.role || 'viewer';
      $('#register-loading').hidden = true;
      $('#register-form').hidden = false;
      $('#register-username').focus();
    } catch (e) {
      invalid(e.status === 429 || e.status === 0 || e.status >= 500
        ? e.message : 'This invite is invalid, expired or already used.');
    }
  }

  async function submit(ev) {
    ev.preventDefault();
    const username = $('#register-username').value.trim();
    const password = $('#register-password').value;
    if (!NAME_RE.test(username)) { formError('Username must be 3 to 32 letters, numbers, dots, dashes or underscores.'); return; }
    if (password.length < 8) { formError('Password must be at least 8 characters.'); return; }
    if (password !== $('#register-confirm').value) { formError('Passwords do not match.'); return; }
    formError('');
    const btn = $('#register-submit');
    btn.disabled = true;
    try {
      await api('POST', '/api/register', { code, username, password });
      location.replace('/');
    } catch (e) {
      const msg = e.status === 409 ? 'That username is already taken. Pick another.'
        : e.status === 410 ? 'This invite has already been used or has expired.'
        : e.message;
      formError(msg);
      btn.disabled = false;
    }
  }

  $('#register-form').addEventListener('submit', submit);
  init();
})();

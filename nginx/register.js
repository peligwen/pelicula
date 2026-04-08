// register.js — self-service viewer registration page logic
(function () {
  'use strict';

  const params = new URLSearchParams(window.location.search);
  const token = params.get('t') || '';

  // ── Boot: validate the token before showing the form ─────────────────────
  async function boot() {
    if (!token) {
      showDead('Invalid link', 'This link is missing a token. Make sure you copied the full URL.');
      return;
    }

    // Validate token format (43 URL-safe base64 chars)
    if (!/^[A-Za-z0-9\-_]{43}$/.test(token)) {
      showDead('Invalid link', 'This invite link appears to be malformed.');
      return;
    }

    try {
      const resp = await fetch(`/api/pelicula/invites/${encodeURIComponent(token)}/check`);
      const data = await resp.json().catch(() => ({}));
      if (!resp.ok) {
        const state = data.state || '';
        if (state === 'expired') {
          showDead('Link expired', 'This invite link has expired. Ask for a new one.');
        } else if (state === 'exhausted') {
          showDead('Link used', 'This invite link has already been used.');
        } else if (state === 'revoked') {
          showDead('Link deactivated', 'This invite link has been deactivated.');
        } else {
          showDead('Link not found', 'This invite link is invalid or has already been used.');
        }
        return;
      }
    } catch (e) {
      showDead('Cannot reach server', 'Could not connect to Pelicula. Try again in a moment.');
      return;
    }

    // Token is valid — hide the loading state, show the registration form.
    document.getElementById('reg-loading').style.display = 'none';
    document.getElementById('reg-form-wrap').style.display = '';
  }

  function showDead(title, text) {
    document.getElementById('reg-loading').style.display = 'none';
    document.getElementById('reg-form-wrap').style.display = 'none';
    document.getElementById('reg-success').style.display = 'none';
    document.getElementById('reg-dead-title').textContent = title;
    document.getElementById('reg-dead-text').textContent = text;
    document.getElementById('reg-dead').style.display = '';
  }

  function showError(msg, html) {
    const el = document.getElementById('reg-error');
    if (html) {
      el.innerHTML = html;
    } else {
      el.textContent = msg;
    }
    el.style.display = 'block';
  }

  function clearError() {
    const el = document.getElementById('reg-error');
    el.style.display = 'none';
    el.textContent = '';
  }

  // ── Password strength meter ───────────────────────────────────────────────
  document.getElementById('reg-password').addEventListener('input', function () {
    const pw = this.value;
    let score = 0;
    if (pw.length >= 8)  score++;
    if (pw.length >= 12) score++;
    if (/[A-Z]/.test(pw)) score++;
    if (/[0-9]/.test(pw)) score++;
    if (/[^A-Za-z0-9]/.test(pw)) score++;
    const bar = document.getElementById('reg-strength-bar');
    const pct = Math.min(score / 4, 1) * 100;
    bar.style.width = pct + '%';
    bar.style.background = score <= 1 ? '#f87171' : score <= 2 ? '#f0c060' : '#7dda93';
  });

  // ── Form submission ───────────────────────────────────────────────────────
  document.getElementById('reg-form').addEventListener('submit', async function (e) {
    e.preventDefault();
    clearError();

    const username = document.getElementById('reg-username').value.trim();
    const password = document.getElementById('reg-password').value;
    const confirm  = document.getElementById('reg-confirm').value;

    if (!username) { showError('Username is required.'); return; }
    if (!password) { showError('Password is required.'); return; }
    if (password !== confirm) { showError('Passwords do not match.'); return; }
    if (password.length < 6) { showError('Password must be at least 6 characters.'); return; }

    const btn = document.getElementById('reg-submit');
    btn.disabled = true;
    btn.textContent = 'Creating account…';

    try {
      const resp = await fetch(`/api/pelicula/invites/${encodeURIComponent(token)}/redeem`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      const data = await resp.json().catch(() => ({}));

      if (resp.ok) {
        showSuccess();
        return;
      }

      if (resp.status === 409 && data.code === 'username_taken') {
        showError('', 'That username is already taken. Try a different one, or sign in to the dashboard if you already have an account.');
        return;
      }
      if (resp.status === 410) {
        showDead('Link no longer active', 'This invite was used up or expired while you were filling in the form. Ask for a new link.');
        return;
      }
      if (resp.status === 429) {
        showError('Too many attempts. Please wait a few minutes and try again.');
        return;
      }
      showError(data.error || 'Something went wrong. Please try again.');
    } catch (err) {
      showError('Network error. Check your connection and try again.');
    } finally {
      btn.disabled = false;
      btn.textContent = 'Create account';
    }
  });

  function showSuccess() {
    document.getElementById('reg-form-wrap').style.display = 'none';
    document.getElementById('reg-success').style.display = 'block';
  }

  boot();
})();

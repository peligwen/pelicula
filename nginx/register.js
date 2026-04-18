// register.js — self-service viewer registration page logic
(function () {
  'use strict';

  const params = new URLSearchParams(window.location.search);
  const token = params.get('t') || '';
  let openRegMode = false;
  let initialSetupMode = false;

  // ── Boot: validate the token (or check open registration) ────────────────
  async function boot() {
    if (!token) {
      // No invite token — check if open registration is enabled
      try {
        const resp = await fetch('/api/pelicula/register/check');
        const data = await resp.json().catch(() => ({}));
        if (data.initial_setup) {
          openRegMode = true;
          initialSetupMode = true;
          document.getElementById('reg-loading').style.display = 'none';
          document.getElementById('reg-form-wrap').style.display = '';
          const heading = document.querySelector('.reg-heading');
          if (heading) heading.textContent = 'Create Admin Account';
          const hint = document.querySelector('.reg-hint');
          if (hint) hint.textContent = 'Create your admin account. You\u2019ll use these credentials to log in and manage everything.';
          suggestPassword();
          return;
        } else if (data.open_registration) {
          openRegMode = true;
          document.getElementById('reg-loading').style.display = 'none';
          document.getElementById('reg-form-wrap').style.display = '';
          const hint = document.querySelector('.reg-hint');
          if (hint) hint.textContent = 'Create a viewer account. You\u2019ll use these credentials to log in.';
          suggestPassword();
          return;
        }
      } catch (e) {
        // fall through
      }
      showDead('Registration closed', 'Open registration is not enabled. Ask an admin for an invite link.');
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
    suggestPassword();
  }

  function showDead(title, text) {
    document.getElementById('reg-loading').style.display = 'none';
    document.getElementById('reg-form-wrap').style.display = 'none';
    document.getElementById('reg-success').style.display = 'none';
    document.getElementById('reg-dead-title').textContent = title;
    document.getElementById('reg-dead-text').textContent = text;
    document.getElementById('reg-dead').style.display = '';
  }

  function showError(msg) {
    const el = document.getElementById('reg-error');
    el.textContent = msg;
    el.style.display = 'block';
  }

  function clearError() {
    const el = document.getElementById('reg-error');
    el.style.display = 'none';
    el.textContent = '';
  }

  // ── Suggest password ─────────────────────────────────────────────────────
  async function suggestPassword() {
    try {
      const resp = await fetch('/api/pelicula/generate-password');
      if (!resp.ok) return;
      const { password } = await resp.json();
      const pwField = document.getElementById('reg-password');
      const cfField = document.getElementById('reg-confirm');
      pwField.value = password;
      cfField.value = password;
      // Show in plain text so the user can note it
      pwField.type = 'text';
      pwField.dispatchEvent(new Event('input'));
    } catch (_) {}
  }

  document.getElementById('suggest-pw').addEventListener('click', function (e) {
    e.preventDefault();
    suggestPassword();
  });

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
    const lvl = score === 0 ? 0 : score === 1 ? 1 : score === 2 ? 2 : score <= 3 ? 3 : 4;
    bar.className = lvl > 0 ? 'strength-bar strength-' + lvl : 'strength-bar';
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
      const url = openRegMode
        ? '/api/pelicula/register'
        : `/api/pelicula/invites/${encodeURIComponent(token)}/redeem`;
      const resp = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      const data = await resp.json().catch(() => ({}));

      if (resp.ok) {
        if (initialSetupMode) {
          // Auto-login after initial setup registration
          try {
            const loginResp = await fetch('/api/pelicula/auth/login', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ username, password }),
            });
            if (loginResp.ok) {
              window.location.href = '/';
              return;
            }
          } catch (e) {
            console.error('auto-login after registration failed', e);
          }
        }
        showSuccess();
        return;
      }

      if (resp.status === 409 && data.code === 'username_taken') {
        showError('That username is already taken. Try a different one, or sign in to the dashboard if you already have an account.');
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

  // ── Password visibility toggle ────────────────────────────────────────────
  document.getElementById('pw-toggle').addEventListener('click', function () {
    const pwField = document.getElementById('reg-password');
    const visible = pwField.type === 'text';
    pwField.type = visible ? 'password' : 'text';
    this.textContent = visible ? '👁' : '🙈';
  });

  boot();
})();

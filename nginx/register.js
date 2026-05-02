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
          if (hint) hint.textContent = 'Create your admin account. You’ll use these credentials to log in and manage everything.';
          suggestPassword();
          document.getElementById('reg-username').focus();
          return;
        } else if (data.open_registration) {
          openRegMode = true;
          document.getElementById('reg-loading').style.display = 'none';
          document.getElementById('reg-form-wrap').style.display = '';
          const hint = document.querySelector('.reg-hint');
          if (hint) hint.textContent = 'Create a viewer account. You’ll use these credentials to log in.';
          suggestPassword();
          document.getElementById('reg-username').focus();
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
    document.getElementById('reg-username').focus();
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
    el.classList.add('show');
  }

  function clearError() {
    const el = document.getElementById('reg-error');
    el.classList.remove('show');
    el.textContent = '';
  }

  // Mirrors clients.IsValidUsername in middleware/clients/jellyfin.go:
  // 1–64 chars, no leading/trailing whitespace, no control chars, no / or \.
  function isValidUsername(s) {
    if (s.length === 0 || s.length > 64) return false;
    if (s !== s.trim()) return false;
    for (const ch of s) {
      const cp = ch.codePointAt(0);
      if (cp < 0x20 || cp === 0x7f) return false;   // C0 controls + DEL
      if (cp >= 0x80 && cp <= 0x9f) return false;    // C1 controls
      if (ch === '/' || ch === '\\') return false;
    }
    return true;
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

    const username = document.getElementById('reg-username').value;
    const password = document.getElementById('reg-password').value;
    const confirm  = document.getElementById('reg-confirm').value;

    if (!username) { showError('Username is required.'); return; }
    if (!isValidUsername(username)) {
      if (username !== username.trim()) {
        showError('Username must not have leading or trailing spaces.');
      } else if (username.length > 64) {
        showError('Username must be 64 characters or fewer.');
      } else {
        showError('Username contains invalid characters (no / or \\ allowed).');
      }
      return;
    }
    if (!password) { showError('Password is required.'); return; }
    if (password !== confirm) { showError('Passwords do not match.'); return; }
    if (password.length < 6) { showError('Password must be at least 6 characters.'); return; }

    const btn = document.getElementById('reg-submit');
    btn.disabled = true;
    btn.textContent = 'Creating account…';

    const ctrl = new AbortController();
    const timeoutId = setTimeout(() => ctrl.abort(), 15000);
    let succeeded = false;
    try {
      const url = openRegMode
        ? '/api/pelicula/register'
        : `/api/pelicula/invites/${encodeURIComponent(token)}/redeem`;
      const resp = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
        signal: ctrl.signal,
      });
      clearTimeout(timeoutId);
      const data = await resp.json().catch(() => ({}));

      if (resp.ok) {
        if (initialSetupMode) {
          const loginCtrl = new AbortController();
          const loginTimeoutId = setTimeout(() => loginCtrl.abort(), 15000);
          try {
            const loginResp = await fetch('/api/pelicula/auth/login', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ username, password }),
              signal: loginCtrl.signal,
            });
            clearTimeout(loginTimeoutId);
            if (loginResp.ok) {
              succeeded = true;
              window.location.href = '/';
              return;
            }
          } catch (loginErr) {
            clearTimeout(loginTimeoutId);
          }
          // Registration succeeded but auto-login failed — surface actionable message.
          succeeded = true;
          showError('Account created but auto-login failed — please log in manually.');
          setTimeout(() => { window.location.href = '/?login=1'; }, 2000);
          return;
        }
        succeeded = true;
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
      clearTimeout(timeoutId);
      if (err.name === 'AbortError') {
        showError('Request timed out — please try again.');
      } else {
        showError('Network error — please check your connection.');
      }
    } finally {
      if (!succeeded) {
        btn.disabled = false;
        btn.textContent = 'Create account';
      }
    }
  });

  function showSuccess() {
    document.getElementById('reg-form-wrap').style.display = 'none';
    document.getElementById('reg-success').style.display = 'block';
    populateNativeAppHint();
  }

  // Fetch the LAN URL for the native-app hint. Public, non-secret endpoint.
  async function populateNativeAppHint() {
    try {
      const resp = await fetch('/api/pelicula/jellyfin/info');
      if (!resp.ok) return;
      const data = await resp.json().catch(() => ({}));
      const lanUrl = (data && data.lan_url) || '';
      if (!lanUrl) return; // no LAN URL configured — leave hint hidden
      const wrap = document.getElementById('reg-native-app');
      const code = document.getElementById('reg-native-url');
      const btn = document.getElementById('reg-copy-url');
      if (!wrap || !code || !btn) return;
      code.textContent = lanUrl;
      wrap.style.display = '';
      btn.addEventListener('click', async function () {
        try {
          await navigator.clipboard.writeText(lanUrl);
          const orig = btn.textContent;
          btn.textContent = 'Copied!';
          setTimeout(function () { btn.textContent = orig; }, 1500);
        } catch (_) {
          // Fallback: select the text so the user can copy manually
          const range = document.createRange();
          range.selectNodeContents(code);
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
        }
      });
    } catch (_) {}
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

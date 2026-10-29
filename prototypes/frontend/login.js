'use strict';
// Only the server establishes identity. No demo identity or browser token storage.
const status = document.querySelector('#login-status');
const google = document.querySelector('#google-login');
const signedOut = document.querySelector('#signed-out');
const signedIn = document.querySelector('#signed-in');
const retry = document.querySelector('#retry');
const logout = document.querySelector('#logout');
let session;
let pending = false;

function showSignedOut(message) {
  session = undefined;
  signedIn.hidden = true;
  document.querySelector('#account-name').textContent = '';
  signedOut.hidden = false;
  google.hidden = location.protocol !== 'https:';
  google.removeAttribute('aria-disabled');
  google.textContent = 'Continue with Google';
  status.textContent = google.hidden
    ? `${message} Google sign-in is available on the live site; this local preview cannot complete sign-in.`
    : message;
}

async function loadSession() {
  if (pending) return;
  pending = true;
  session = undefined;
  signedIn.hidden = signedOut.hidden = retry.hidden = true;
  status.textContent = 'Checking your sign-in status…';
  try {
    const response = await fetch('/api/me', { credentials: 'same-origin', cache: 'no-store', signal: AbortSignal.timeout(10000) });
    if (response.status === 401) {
      showSignedOut(new URLSearchParams(location.search).get('login') === 'cancelled'
        ? 'Google sign-in was cancelled. You can try again.' : 'Sign in with your Google account.');
      return;
    }
    if (!response.ok) {
      status.textContent = response.status === 404 ? 'Sign-in is not available in this preview. You can still explore the work.'
        : response.status === 403 ? 'This account cannot sign in. You can still browse without signing in.'
        : 'We couldn’t check your account. Please try again.';
      retry.hidden = false;
      return;
    }
    const value = await response.json();
    if (!value?.account?.id || typeof value.account.displayName !== 'string' || !['user', 'admin'].includes(value.role)
      || typeof value.csrfToken !== 'string' || !value.csrfToken) throw new Error('Invalid session response');
    session = value;
    document.querySelector('#account-name').textContent = value.account.displayName;
    signedIn.hidden = false;
    status.textContent = new URLSearchParams(location.search).get('login') === 'cancelled'
      ? 'Google sign-in was cancelled. Your existing account is still signed in.' : 'You’re signed in.';
  } catch {
    status.textContent = 'We couldn’t check your account. Check your connection and try again.';
    retry.hidden = false;
  } finally { pending = false; }
}

google.onclick = event => {
  if (google.getAttribute('aria-disabled') === 'true') { event.preventDefault(); return; }
  google.setAttribute('aria-disabled', 'true');
  google.textContent = 'Opening Google…';
  status.textContent = 'Continue on Google to finish signing in.';
  // Native top-level navigation follows the server’s OAuth redirect.
};
retry.onclick = loadSession;
logout.onclick = async () => {
  if (pending || !session) return;
  pending = true;
  logout.disabled = true;
  status.textContent = 'Signing out…';
  try {
    const response = await fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin',
      headers: { 'X-CSRF-Token': session.csrfToken }, signal: AbortSignal.timeout(10000) });
    if (response.status === 204 || response.status === 401) {
      showSignedOut(response.status === 204 ? 'You’re signed out.' : 'Your session has expired. Please sign in again.');
    } else {
      status.textContent = 'Sign-out was not confirmed. Check your account before trying again.';
      retry.hidden = false;
    }
  } catch {
    status.textContent = 'Sign-out was not confirmed. Check your connection, then check your account.';
    retry.hidden = false;
  } finally { pending = false; logout.disabled = false; }
};
window.addEventListener('pageshow', loadSession);

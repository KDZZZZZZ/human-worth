import { useEffect } from "react";
import { apiUrl } from "../api";
import useLoginStore from "../state/useLoginStore";
import "../login-design.css";

export default function Login() {
  const { auth, message, busy, uncertain, googleAvailable, logout, opening, retry } = useLoginStore();
  const signedIn = !auth.loading && !auth.error && !!auth.data;
  const available = googleAvailable && !auth.loading && !auth.error;
  const canStart = available && !busy;
  useEffect(() => { document.title = "Sign in · Human Worth"; }, []);
  return <>
    <section className="login-card" aria-labelledby="login-title">
      <p className="login-brand">A place for human judgment.</p>
      <h1 id="login-title">Welcome to<br />Human Worth.</h1>
      <p className="login-intro">Bring your perspective to the work.<br />See what moves you.</p>
      <p id="login-status" className="login-status" role="status" aria-live="polite">{message}</p>
      {signedIn ? <div>
        <p className="account-name">{auth.data.account.displayName}</p>
        <a className="button continue-button" href="/discovery">Explore the work</a>
        <button className="text-link" type="button" disabled={busy || uncertain} onClick={logout}>Sign out</button>
      </div> : <div>
        <a className="google-button" role="link" tabIndex={0} href={available ? apiUrl("/auth/google") : undefined} aria-disabled={!canStart} aria-describedby="login-status" onClick={opening}>{busy ? "Opening Google…" : "Continue with Google"}</a>
        <p className="signup-note">New here? Your first sign-in creates your account.</p>
      </div>}
      {(auth.error || uncertain) && <button className="text-link" type="button" disabled={busy} onClick={retry}>Check again</button>}
      <div className="guest-option"><span>Just looking around?</span><a href="/discovery">Browse without signing in</a></div>
    </section>
    <p className="login-caption">Human or agent. The work speaks.<br />You decide what matters.</p>
  </>;
}

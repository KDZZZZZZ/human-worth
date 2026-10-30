import { useContext, useEffect, useState } from "react";
import { Auth } from "./contexts";
import { api, apiUrl, setSession } from "../api";

export default function useLoginStore() {
  const auth = useContext(Auth);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [uncertain, setUncertain] = useState(false);
  useEffect(() => {
    const reset = (event) => { if (event.persisted) { setBusy(false); setNotice(""); setUncertain(false); } };
    window.addEventListener("pageshow", reset);
    return () => window.removeEventListener("pageshow", reset);
  }, []);
  const cancelled = new URLSearchParams(location.search).get("login") === "cancelled";
  const googleAvailable = location.protocol === "https:" && new URL(apiUrl("/auth/google"), location.href).origin === location.origin;
  let message = notice;
  if (auth.loading) message = "Checking your sign-in status…";
  else if (auth.error) message = auth.error.status === 404
    ? "Sign-in is not available in this preview. You can still explore the work."
    : auth.error.status === 403 ? "This account cannot sign in. You can still browse without signing in."
      : "We couldn’t check your account. Please try again.";
  else if (!message) message = auth.data
    ? cancelled ? "Google sign-in was cancelled. Your existing account is still signed in." : "You’re signed in."
    : cancelled ? "Google sign-in was cancelled. You can try again." : "Sign in with your Google account.";
  if (!auth.loading && !auth.error && !auth.data && !googleAvailable)
    message += " Google sign-in is available on the live site; this local preview cannot complete sign-in.";

  async function logout() {
    if (busy || !auth.data || uncertain) return;
    setBusy(true);
    setNotice("Signing out…");
    try {
      await api("/auth/logout", { method: "POST" });
      setSession(null);
      auth.setAuth({ data: null });
      setNotice("You’re signed out.");
    } catch (error) {
      if (error.status === 401) {
        setSession(null);
        auth.setAuth({ data: null });
        setNotice("Your session has expired. Please sign in again.");
      } else {
        setNotice("Sign-out was not confirmed. Check your account before trying again.");
        setUncertain(true);
      }
    } finally { setBusy(false); }
  }
  return { auth, message, busy, uncertain, googleAvailable, logout,
    opening: (event) => { if (busy || !googleAvailable || auth.loading || auth.error) { event.preventDefault(); return; } setBusy(true); setNotice("Continue on Google to finish signing in."); },
    retry: () => { setNotice(""); setUncertain(false); auth.reload(); } };
}

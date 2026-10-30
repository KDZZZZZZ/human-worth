import { useState, useEffect } from "react";
import { Auth } from "./contexts";
import { api, cachedSession, setSession } from "../api";
export function SessionProvider({ children }) {
  const [auth, setAuth] = useState(() => {
      const data = cachedSession();
      // Show the last known account immediately; /me still revalidates below.
      return data ? { data, loading: true } : { loading: true };
    }),
    [version, setVersion] = useState(0);
  useEffect(() => {
    const c = new AbortController();
    api("/me", { signal: c.signal })
      .then((data) => {
        if (c.signal.aborted) return;
        if (!data?.account?.id || typeof data.account.displayName !== "string" || !["user", "admin"].includes(data.role) || typeof data.csrfToken !== "string" || !data.csrfToken)
          throw new Error("Invalid session response");
        setSession(data);
        setAuth({ data });
      })
      .catch((error) => {
        if (c.signal.aborted) return;
        setSession(null);
        setAuth(error.status === 401 ? { data: null } : { error });
      });
    return () => c.abort();
  }, [version]);
  useEffect(() => {
    const revalidate = (event) => { if (event.persisted) { setAuth({ loading: true }); setVersion((value) => value + 1); } };
    window.addEventListener("pageshow", revalidate);
    return () => window.removeEventListener("pageshow", revalidate);
  }, []);
  useEffect(() => {
    const expired = () => { setSession(null); setAuth({ data: null }); };
    window.addEventListener('human-worth:session-expired', expired);
    return () => window.removeEventListener('human-worth:session-expired', expired);
  }, []);
  const value = { ...auth, reload: () => { setAuth({ loading: true }); setVersion((x) => x + 1); } };
  return (
    <Auth.Provider value={{ ...value, setAuth }}>{children}</Auth.Provider>
  );
}

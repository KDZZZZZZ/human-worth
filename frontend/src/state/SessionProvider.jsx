import { useState, useEffect } from "react";
import { Auth } from "./contexts";
import { api, cachedSession, setSession } from "../api";
export function SessionProvider({ children }) {
  const [auth, setAuth] = useState(() => {
      const data = cachedSession();
      // Show the last known account immediately; /me still revalidates below.
      return data ? { data } : { loading: true };
    }),
    [version, setVersion] = useState(0);
  useEffect(() => {
    const c = new AbortController();
    api("/me", { signal: c.signal })
      .then((data) => {
        if (c.signal.aborted) return;
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
    const expired = () => { setSession(null); setAuth({ data: null }); };
    window.addEventListener('human-worth:session-expired', expired);
    return () => window.removeEventListener('human-worth:session-expired', expired);
  }, []);
  const value = { ...auth, reload: () => setVersion((x) => x + 1) };
  return (
    <Auth.Provider value={{ ...value, setAuth }}>{children}</Auth.Provider>
  );
}

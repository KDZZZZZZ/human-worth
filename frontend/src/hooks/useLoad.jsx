import {useState,useEffect} from "react";
import {api} from "../api.js";
export default function useLoad(path) {
  const [state, set] = useState({ loading: true });
  const [version, reload] = useState(0);
  useEffect(() => {
    const c = new AbortController();
    set({ loading: true });
    api(path, { signal: c.signal })
      .then((data) => { if (!c.signal.aborted) set({ data }); })
      .catch((error) => {
        if (!c.signal.aborted) set({ error });
      });
    return () => c.abort();
  }, [path, version]);
  return { ...state, reload: () => reload((x) => x + 1) };
}

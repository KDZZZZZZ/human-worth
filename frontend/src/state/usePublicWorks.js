import { useEffect, useState } from "react";
import { api, enc } from "../api";

// Public reading never depends on a session, eligibility or a ballot.
export default function usePublicWorks(taskId) {
  const [items, setItems] = useState([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState(null);
  const [version, setVersion] = useState(0);
  useEffect(() => { setItems([]); }, [taskId]);
  useEffect(() => {
    const controller = new AbortController();
    async function load() {
      setBusy(true);
      setError(null);
      try {
        const works = new Map(), cursors = new Set();
        let cursor = null;
        do {
          const query = new URLSearchParams({ limit: "100", ...(cursor ? { cursor } : {}) });
          const page = await api(`/tasks/${enc(taskId)}/entries?${query}`, { signal: controller.signal });
          if (!Array.isArray(page.items)) throw new Error("Invalid works response. Please retry.");
          page.items.forEach((work) => works.set(work.id, work));
          cursor = page.nextCursor;
          if (cursor && cursors.has(cursor)) throw new Error("Could not load all works. Please retry.");
          if (cursor) cursors.add(cursor);
        } while (cursor);
        if (!controller.signal.aborted) setItems([...works.values()]);
      } catch (e) {
        if (!controller.signal.aborted) setError(e);
      } finally {
        if (!controller.signal.aborted) setBusy(false);
      }
    }
    load();
    return () => controller.abort();
  }, [taskId, version]);
  return { items, busy, error, reload: () => setVersion((value) => value + 1) };
}

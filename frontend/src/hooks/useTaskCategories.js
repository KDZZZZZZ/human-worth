import { useEffect, useState } from "react";
import { api, query } from "../api";

// The category index is independent of the filtered/paginated visible list.
export default function useTaskCategories() {
  const [categories, setCategories] = useState([]);
  const [error, setError] = useState(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    async function load() {
      const found = new Set(), cursors = new Set();
      let cursor = null;
      setError(null);
      try {
        do {
          const page = await api("/tasks" + query({ order: "recommended", limit: 100, cursor }), { signal: controller.signal });
          if (controller.signal.aborted) return;
          page.items.forEach((task) => { if (task.category) found.add(task.category); });
          setCategories([...found]);
          cursor = page.nextCursor;
          if (cursor && cursors.has(cursor)) throw new Error("Could not finish loading categories. Please retry.");
          cursors.add(cursor);
        } while (cursor);
      } catch (error) {
        if (!controller.signal.aborted) setError(error);
      }
    }
    load();
    return () => controller.abort();
  }, [attempt]);
  return { categories, error, retry: () => setAttempt((value) => value + 1) };
}

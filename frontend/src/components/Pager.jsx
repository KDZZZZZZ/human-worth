import { useState, useEffect, useRef } from "react";
import { api, id } from "../api.js";
import ErrorNote from "./ErrorNote.jsx";
export default function Pager({
  path,
  render,
  empty = "No items yet.",
  moreLabel = "Load more",
  auto = false,
  onItems,
  limit = 20,
  initialLimit = limit,
  moreClassName = "text-link",
}) {
  const [items, setItems] = useState([]),
    [cursor, setCursor] = useState(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(null);
  const epoch = useRef(0),
    sentinel = useRef(null),
    busyRef = useRef(false);
  async function load(next = null, version = epoch.current) {
    if (busyRef.current) return;
    busyRef.current = true;
    setBusy(true);
    setError(null);
    try {
      const page = await api(
        path +
          (path.includes("?") ? "&" : "?") +
          new URLSearchParams({
            limit: String(next ? limit : initialLimit),
            ...(next ? { cursor: next } : {}),
          }),
      );
      if (version !== epoch.current) return;
      setItems((old) =>
        next
          ? [...new Map([...old, ...page.items].map((x) => [x.id, x])).values()]
          : page.items,
      );
      setCursor(page.nextCursor);
      onItems?.(page.items);
    } catch (e) {
      if (version === epoch.current) setError(e);
    } finally {
      if (version === epoch.current) {
        busyRef.current = false;
        setBusy(false);
      }
    }
  }
  useEffect(() => {
    epoch.current++;
    busyRef.current = false;
    setItems([]);
    setCursor(null);
    load(null, epoch.current);
    return () => {
      epoch.current++;
      busyRef.current = false;
    };
  }, [path]);
  useEffect(() => {
    if (!auto || !cursor || busy || error) return;
    const observer = new IntersectionObserver((entries) => {
      if (entries[0].isIntersecting) load(cursor);
    });
    if (sentinel.current) observer.observe(sentinel.current);
    return () => observer.disconnect();
  }, [auto, cursor, busy, error]);
  return (
    <>
      {items.map(render)}
      {!busy &&
        !error &&
        !items.length &&
        (typeof empty === "string" ? (
          <p className="state-note">{empty}</p>
        ) : (
          empty
        ))}
      <ErrorNote error={error} retry={() => load(cursor)} />
      <div ref={sentinel} className="list-end">
        {busy ? (
          <p role="status">Loading…</p>
        ) : (
          cursor && (
            <button className={moreClassName} onClick={() => load(cursor)}>
              {moreLabel}
            </button>
          )
        )}
      </div>
    </>
  );
}

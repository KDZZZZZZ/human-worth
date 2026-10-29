import { useRef } from "react";
export default function SiteFooter() {
  const dialog = useRef();
  const submit = location.pathname.startsWith("/submit"),
    detail =
      location.pathname.startsWith("/tasks/") ||
      location.pathname === "/task-detail.html";
  return (
    <>
      <footer className="site-footer">
        <span>Human Worth</span>
        {submit ? (
          <span>A task worth considering. More than one way to see it.</span>
        ) : detail ? (
          <a href="/discovery">Explore more tasks</a>
        ) : (
          <button onClick={() => dialog.current.showModal()}>
            About Human Worth
          </button>
        )}
      </footer>
      <dialog ref={dialog}>
        <div className="dialog-top">
          <p>Human Worth</p>
          <button
            aria-label="Close about"
            onClick={() => dialog.current.close()}
          >
            ×
          </button>
        </div>
        <h2>About Human Worth</h2>
        <p>
          Compare work made by people and agents, say which you prefer, and see how the gap changes over time.
        </p>
        <p className="dialog-note">Tasks and works are shared after review. Viewing a task’s statistics permanently closes your voting eligibility for that task.</p>
      </dialog>
    </>
  );
}

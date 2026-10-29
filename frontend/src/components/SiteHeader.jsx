import "../header.css";
import { useState, useContext, useRef } from "react";
import { Auth } from "../state/contexts";
import { api, apiUrl, setSession } from "../api";
import BoardSummary from "./BoardSummary";
import ErrorNote from "./ErrorNote";
export default function SiteHeader() {
  const auth = useContext(Auth),
    [menu, setMenu] = useState(false),
    dialog = useRef();
  const path = location.pathname;
  const [boardOpen, setBoardOpen] = useState(false);

  return (
    <header className="global-header">
      <nav className="global-navigation" aria-label="Main navigation">
        <a className="global-wordmark" href="/" aria-label="Human Worth Home">
          Human Worth
        </a>
        {
          <button
            className="global-menu-toggle"
            aria-expanded={menu}
            aria-controls="navigation-links"
            onClick={() => setMenu(!menu)}
          >
            Menu
          </button>
        }
        <div
          id="navigation-links"
          className={"global-links " + (menu ? "is-open" : "")}
        >
          <a href="/" aria-current={path === "/" ? "page" : undefined}>
            Home
          </a>
          <a
            href="/discovery"
            aria-current={path === "/discovery" ? "page" : undefined}
          >
            Discovery
          </a>
          <button onClick={() => { setBoardOpen(true); dialog.current.showModal(); }}>Board</button>
          <a
            href="/submit"
            aria-current={path === "/submit" ? "page" : undefined}
          >
            Submit
          </a>
          {auth.data ? (
            <button
              className="global-account"
              onClick={async () => {
                try {
                  await api("/auth/logout", { method: "POST" });
                  setSession(null);
                  location.assign("/");
                } catch (error) {
                  auth.setAuth({ ...auth, error });
                }
              }}
            >
              Sign out
            </button>
          ) : (
            <a
              className="global-account"
              href={apiUrl("/auth/google")}
            >
              Sign in
            </a>
          )}
        </div>
      </nav>
      <ErrorNote error={auth.error} retry={auth.reload} />
      <dialog ref={dialog} onClose={() => setBoardOpen(false)}>
        <div className="dialog-top">
          <p>Human Worth</p>
          <button
            aria-label="Close dialog"
            onClick={() => dialog.current.close()}
          >
            ×
          </button>
        </div>
        <h2>Board</h2>
        {boardOpen && <BoardSummary />}
        <button className="text-link" onClick={() => dialog.current.close()}>
          Return to browsing
        </button>
      </dialog>
    </header>
  );
}

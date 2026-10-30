import "../header.css";
import { useState, useContext } from "react";
import { Auth } from "../state/contexts";
import { api, setSession } from "../api";
import ErrorNote from "./ErrorNote";
export default function SiteHeader() {
  const auth = useContext(Auth),
    [menu, setMenu] = useState(false);
  const path = location.pathname;
  const login = path === "/login" || path === "/login.html" || (path === "/" && new URLSearchParams(location.search).get("login") === "cancelled");
  if (login) return <header className="global-header"><nav className="global-navigation" aria-label="Main navigation"><a className="global-wordmark" href="/">Human Worth</a><a className="browse-link" href="/discovery">Explore the work</a></nav></header>;

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
          <a href="/board" aria-current={path === "/board" || path === "/board.html" ? "page" : undefined}>Board</a>
          <a
            href="/submit"
            aria-current={path === "/submit" ? "page" : undefined}
          >
            Submit
          </a>
          {auth.data ? (
            <button
              className="global-account"
              disabled={auth.loading}
              onClick={async () => {
                try {
                  await api("/auth/logout", { method: "POST" });
                  setSession(null);
                  location.assign("/");
                } catch (error) {
                  auth.setAuth(error.status === 401 ? { data: null } : { ...auth, error });
                }
              }}
            >
              Sign out
            </button>
          ) : (
            <a
              className="global-account"
              href="/login"
            >
              Sign in
            </a>
          )}
        </div>
      </nav>
      <ErrorNote error={auth.error} retry={auth.reload} />
    </header>
  );
}

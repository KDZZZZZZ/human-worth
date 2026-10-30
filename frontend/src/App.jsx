import SiteFooter from "./components/SiteFooter";
import SiteHeader from "./components/SiteHeader";
import Home from "./pages/Home";
import Discovery from "./pages/Discovery";
import Task from "./pages/Task";
import Submit from "./pages/Submit";
import MySubmissions from "./pages/MySubmissions";
import Board from "./pages/Board";
import Login from "./pages/Login";
import usePageTransition from "./hooks/usePageTransition";
import { lazy, Suspense } from "react";
const ImportDesign = import.meta.env.DEV ? lazy(() => import("./pages/ImportDesign")) : null;
export default function App() {
  usePageTransition();
  const path = location.pathname;
  const login = path === "/login" || path === "/login.html" || (path === "/" && new URLSearchParams(location.search).get("login") === "cancelled");
  const board = path === "/board" || path === "/board.html";
  const home = !login && (path === "/" || path === "/index.html");
  return (
    <div
      className={
        login ? "login-page" : board ? "board-site" : home
          ? "home-site"
          : path.startsWith("/submit")
            ? "submit-site"
            : path.startsWith("/tasks/") || path === "/task-detail.html"
              ? "detail-site"
              : "app-site"
      }
    >
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <SiteHeader />
      <main id="main" className={login ? "login-main" : home ? "page-width" : undefined}>
        {login ? <Login /> : board ? <Board /> : ImportDesign && path === "/dev/import-design" ? <Suspense fallback={<p>Loading…</p>}><ImportDesign /></Suspense> : home ? (
          <Home />
        ) : path === "/discovery" || path === "/discovery.html" ? (
          <Discovery />
        ) : path === "/me/submissions" ? <MySubmissions /> : path === "/submit" || path === "/submit.html" ? (
          <Submit />
        ) : path.startsWith("/tasks/") || path === "/task-detail.html" ? (
          <Task
            taskId={
              path.startsWith("/tasks/")
                ? decodeURIComponent(path.split("/")[2])
                : new URLSearchParams(location.search).get("id") ||
                  ""
            }
          />
        ) : (
          <section className="page-state">
            <h1>Page not found</h1>
            <a href="/">Return Home</a>
          </section>
        )}
      </main>
      <SiteFooter />
    </div>
  );
}

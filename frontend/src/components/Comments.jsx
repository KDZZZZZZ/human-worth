import { useState, useRef, useContext } from "react";
import { Auth } from "../state/contexts.js";
import { api, enc, id } from "../api.js";
import ErrorNote from "./ErrorNote.jsx";
import Login from "./Login.jsx";
import Pager from "./Pager.jsx";
import Report from "./Report.jsx";
export default function Comments({ taskId }) {
  const auth = useContext(Auth),
    [body, setBody] = useState(""),
    [error, setError] = useState(null),
    [busy, setBusy] = useState(false),
    [version, setVersion] = useState(0);
  const pending = useRef(null);
  return (
    <section id="discussion" className="section-row">
      <aside className="section-margin">Discussion</aside>
      <div className="section-body">
        <h2>What shaped your choice?</h2>
        <p className="lead">
          Discuss the work and the situation. A different perspective can be
          useful.
        </p>

        <Pager
          empty="No comments yet."
          key={version}
          path={"/tasks/" + enc(taskId) + "/comments"}
          render={(c) => (
            <article className="comment" key={c.id}>
              <p className="small">
                {c.author.displayName} ·{" "}
                {new Date(c.createdAt).toLocaleString()}
              </p>
              <p>{c.body}</p>
              <Report kind="comment" target={c.id} />
            </article>
          )}
        />
        {auth.data ? (
          <form
            onSubmit={async (e) => {
              e.preventDefault();
              if (!body.trim()) return;
              setBusy(true);
              setError(null);
              pending.current ??= { body, key: id() };
              try {
                await api("/tasks/" + enc(taskId) + "/comments", {
                  method: "POST",
                  body: { body: pending.current.body },
                  headers: { "Idempotency-Key": pending.current.key },
                });
                setBody("");
                pending.current = null;
                setVersion((x) => x + 1);
              } catch (e) {
                setError(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            <label>
              Your comment
              <textarea
                placeholder="What made a difference to you?"
                rows={4}
                required
                value={body}
                disabled={busy || !!pending.current}
                onChange={(e) => setBody(e.target.value)}
              />
            </label>
            <button className="button" disabled={busy}>
              {pending.current ? "Retry comment" : "Post comment"}
            </button>
            <ErrorNote error={error} />
          </form>
        ) : (
          <Login />
        )}
      </div>
    </section>
  );
}

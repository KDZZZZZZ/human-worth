import { useState, useContext } from "react";
import { Auth } from "../state/contexts.js";
import { api } from "../api.js";
import ErrorNote from "./ErrorNote.jsx";
import Login from "./Login.jsx";
export default function Report({ kind, target }) {
  const auth = useContext(Auth);
  const [open, setOpen] = useState(false),
    [reason, setReason] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(null),
    [done, setDone] = useState(false);
  return (
    <>
      <button className="text-link" onClick={() => setOpen(!open)}>
        Report {kind === "entry" ? "work" : kind}
      </button>
      {open &&
        (auth.data ? (
          <form
            className="report-form"
            onSubmit={async (e) => {
              e.preventDefault();
              setBusy(true);
              try {
                await api("/reports", {
                  method: "POST",
                  body: { targetKind: kind, targetId: target, reason },
                });
                setDone(true);
              } catch (e) {
                setError(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {done ? (
              <p role="status">{"Report recorded."}</p>
            ) : (
              <>
                <label>
                  Reason
                  <textarea
                    required
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                  />
                </label>
                <button disabled={busy} className="button">
                  {"Submit report"}
                </button>
              </>
            )}
            <ErrorNote error={error} />
          </form>
        ) : (
          <Login />
        ))}
    </>
  );
}

import { Auth } from "./contexts";
import { useState, useEffect, useRef, useContext } from "react";

import { emptyWork } from "./models";
import { api, enc, id } from "../api";
export default function useSubmissionStore() {
  const auth = useContext(Auth);
  const [serverSnapshot, setServerSnapshot] = useState(null);
  const [uploads, setUploads] = useState(0);
  const [params] = useState(() => new URLSearchParams(location.search));
  const task = params.get("task"),
    edit = params.get("edit"),
    entry = params.get("entry");
  const [draft, setDraft] = useState(
      {
            title: "",
            summary: "",
            description: "",
            category: "",
            entries: task ? [emptyWork("human")] : [],
            taskAttachments: [],
          },
    ),
    [record, setRecord] = useState(null),
    [loading, setLoading] = useState(!!edit || !!entry),
    [error, setError] = useState(null),
    [busy, setBusy] = useState(false),
    [dirty, setDirty] = useState(false),
    [preview, setPreview] = useState(false),
    [message, setMessage] = useState("");
  const pending = useRef(null);
  function rememberTask(saved) {
    if (!task && !entry) history.replaceState(null, '', '/submit?edit=' + enc(saved.id));
  }
  useEffect(() => {
    if (!edit && !entry) return;
    api(entry ? "/me/entries/" + enc(entry) : "/me/tasks/" + enc(edit))
      .then((data) => {
        setRecord(data);
        setDraft(entry ? { ...draft, entries: [data.content] } : data.content);
      })
      .catch(setError)
      .finally(() => setLoading(false));
  }, [edit, entry]);
  useEffect(() => {
    const warn = (e) => {
      if (dirty) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);
  const locked = record && !["draft", "rejected"].includes(record.state);
  function change(next) {
    setDraft(next);
    setDirty(true);
    setMessage("");
  }
  async function save(review = false) {
    if (!auth.data) {
      setError(
        Object.assign(new Error("Sign in to save or submit your work."), {
          status: 401,
        }),
      );
      return;
    }
    if (uploads) return;
    if (
      draft.taskAttachments?.some((a) => typeof a.cloudUseAllowed !== "boolean")
    ) {
      setError(new Error("Choose a permission for every task attachment."));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      let saved = record;
      if (
        review &&
        !task &&
        !entry &&
        (!draft.title.trim() ||
          !draft.summary.trim() ||
          !draft.description.trim() ||
          !["human", "agent"].every((side) =>
            draft.entries.some((w) => w.side === side),
          ))
      )
        throw Error(
          "Complete the task information and add at least one Human work and one Agent work before submitting for review.",
        );
      if (task || entry) {
        if (draft.entries.length !== 1) throw Error("Add one work at a time.");
        if (!review) throw Error("Preview the work before submitting for review.");
        if (
          !draft.entries[0].artifacts.some(
            (a) => a.kind === "file" || (a.kind === "text" && a.text.trim()),
          )
        )
          throw Error("Add the actual work or a file. A supplementary link cannot replace the work.");
        pending.current ??= {
          path: entry
            ? "/me/entries/" + enc(entry)
            : "/tasks/" + enc(task) + "/entries",
          method: entry ? "PUT" : "POST",
          body: entry
            ? { expectedRevision: record.revision, content: draft.entries[0] }
            : draft.entries[0],
          key: id(),
        };
        saved = await api(pending.current.path, {
          method: pending.current.method,
          body: pending.current.body,
          headers: { "Idempotency-Key": pending.current.key },
        });
      } else {
        if (!saved || dirty) {
          pending.current ??= {
            path: saved ? "/me/tasks/" + enc(saved.id) : "/tasks",
            method: saved ? "PUT" : "POST",
            body: saved
              ? { expectedRevision: saved.revision, content: draft }
              : draft,
            key: id(),
          };
          saved = await api(pending.current.path, {
            method: pending.current.method,
            body: pending.current.body,
            headers: { "Idempotency-Key": pending.current.key },
          });
          setRecord(saved);
          rememberTask(saved);
          setDirty(false);
          pending.current = null;
        }
        if (review) {
          if (
            !draft.entries.some((w) => w.side === "human") ||
            !draft.entries.some((w) => w.side === "agent")
          )
            throw Error("Add at least one complete Human work and one Agent work before submitting for review.");
          saved = await api("/me/tasks/" + enc(saved.id) + "/review-requests", {
            method: "POST",
            body: { expectedRevision: saved.revision },
          });
        }
      }
      setRecord(saved);
      rememberTask(saved);
      setDirty(false);
      pending.current = null;
      setMessage(
        review
            ? "Submitted for review. Your submission will be shared after review."
            : "Draft saved.",
      );
    } catch (e) {
      if ([400, 413, 415, 422].includes(e.status)) pending.current = null;
      setError(e);
    } finally {
      setBusy(false);
    }
  }
  return {
    serverSnapshot,
    setServerSnapshot,
    uploads,
    setUploads,
    task,
    edit,
    entry,
    draft,
    setDraft,
    record,
    setRecord,
    loading,
    error,
    setError,
    busy,
    dirty,
    setDirty,
    preview,
    setPreview,
    message,
    pending,
    locked,
    change,
    save,
  };
}

import { api, enc } from "../api";
import ReviewMaterials from "./ReviewMaterials";
export default function SubmissionConflict({ store }) {
  const {
    record,
    entry,
    error,
    serverSnapshot,
    setServerSnapshot,
    setError,
    setRecord,
    setDraft,
    draft,
    setDirty,
    pending,
  } = store;
  if (!record || !error) return null;
  return (
    <section>
      <button
        type="button"
        className="text-link"
        onClick={async () => {
          try {
            setServerSnapshot(
              await api(
                entry
                  ? "/me/entries/" + enc(record.id)
                  : "/me/tasks/" + enc(record.id),
              ),
            );
          } catch (e) {
            setError(e);
          }
        }}
      >
        Review latest server version
      </button>
      {serverSnapshot && (
        <div className="review-preview">
          <p>
            Server revision {serverSnapshot.revision} · {serverSnapshot.state}
          </p>
          <ReviewMaterials review={{ id: "", submission: serverSnapshot }} />
          <button
            type="button"
            className="text-link"
            onClick={() => {
              if (confirm("Replace unsaved edits with the server version?")) {
                setRecord(serverSnapshot);
                setDraft(
                  entry
                    ? { ...draft, entries: [serverSnapshot.content] }
                    : serverSnapshot.content,
                );
                setDirty(false);
                pending.current = null;
                setError(null);
                setServerSnapshot(null);
              }
            }}
          >
            Use server version
          </button>
          <button
            type="button"
            className="text-link"
            onClick={() => {
              if (
                confirm(
                  "Keep all local edits using the reviewed revision? The next save will replace the server content.",
                )
              ) {
                setRecord(serverSnapshot);
                setDirty(true);
                pending.current = null;
                setError(null);
                setServerSnapshot(null);
              }
            }}
          >
            Keep local edits with reviewed revision
          </button>
        </div>
      )}
    </section>
  );
}

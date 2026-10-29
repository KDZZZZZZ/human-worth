import { useState, useRef, useContext } from "react";
import { UploadBusy } from "../state/contexts.js";
import { uploadFile } from "../upload.js";
import { api, apiUrl, setSession, enc, id } from "../api.js";
import ErrorNote from "./ErrorNote.jsx";
import FilePicker from "./FilePicker";
export default function TaskAttachments({ value, onChange }) {
  const uploading = useContext(UploadBusy);
  const [session, setSession] = useState(null),
    [progress, setProgress] = useState(""),
    [error, setError] = useState(null),
    [busy, setBusy] = useState(false);
  const latest = useRef(value);
  latest.current = value;
  async function send(file = session?.file) {
    if (!file) return;
    uploading(1);
    setBusy(true);
    setError(null);
    try {
      const asset = await uploadFile(file, session, setSession, setProgress);
      onChange([
        ...latest.current,
        { assetId: asset.id, cloudUseAllowed: null },
      ]);
      setSession(null);
      setProgress("File uploaded: " + asset.filename);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
      uploading(-1);
    }
  }
  return (
    <div className="form-section">
      <h3>Shared task attachments</h3>
      <p className="small">Task attachments and work files have separate review and permissions.</p>
      <label>
        Choose shared files
        <FilePicker
          label="Choose shared files"
          accept="image/png,image/jpeg,image/webp,application/pdf,text/plain"
          disabled={busy || !!session}
          onChange={(e) => send(e.target.files[0])}
        />
      </label>
      <p className="small">
        {"Files are uploaded privately. Choose a cloud-use permission for each attachment."}
      </p>
      <p role="status" className="small">
        {progress}
      </p>
      <ErrorNote error={error} />
      {session && (
        <>
          <button
            type="button"
            disabled={busy}
            className="text-link"
            onClick={() => send()}
          >
            Check progress & resume
          </button>
          <button
            type="button"
            disabled={busy}
            className="text-link"
            onClick={async () => {
              try {
                await api(
                  "/me/assets/upload-sessions/" + enc(session.sessionId),
                  { method: "DELETE" },
                );
                setSession(null);
                setProgress("Upload cancelled.");
                setError(null);
              } catch (e) {
                setError(e);
              }
            }}
          >
            Cancel upload
          </button>
        </>
      )}
      {value.map((a, i) => (
        <div key={a.assetId}>
          <a
            className="text-link"
            href={apiUrl("/me/assets/" + enc(a.assetId) + "/content")}
            target="_blank"
            rel="noopener noreferrer"
          >
            Open attachment {i + 1}
          </a>
          <label>
            Allow this attachment in cloud challenges?
            <select
              required
              value={
                a.cloudUseAllowed === null ? "" : String(a.cloudUseAllowed)
              }
              onChange={(e) =>
                onChange(
                  value.map((x, j) =>
                    i === j
                      ? { ...x, cloudUseAllowed: e.target.value === "true" }
                      : x,
                  ),
                )
              }
            >
              <option value="" disabled>
                Choose a permission
              </option>
              <option value="false">Do not allow</option>
              <option value="true">Allow</option>
            </select>
          </label>
          <button
            type="button"
            className="text-link"
            onClick={() => onChange(value.filter((_, j) => j !== i))}
          >
            Remove attachment
          </button>
        </div>
      ))}
    </div>
  );
}

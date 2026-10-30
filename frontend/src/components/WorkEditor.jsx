import { useState, useRef, useContext } from "react";
import { UploadBusy } from "../state/contexts.js";
import { uploadFile } from "../upload.js";
import { api, enc, id } from "../api.js";
import ErrorNote from "./ErrorNote.jsx";
import FilePicker from "./FilePicker";
import { missingWorkFields } from "../state/models";
export default function WorkEditor({
  work,
  onChange,
  onRemove,
  disabled,
  removable = true,
  allowSideChange = false,
}) {
  const uploading = useContext(UploadBusy);
  const [upload, setUpload] = useState(null),
    [progress, setProgress] = useState(""),
    [error, setError] = useState(null);
  const latest = useRef(work);
  latest.current = work;
  const change = (key, value) => onChange({ ...latest.current, [key]: value });
  const text = work.artifacts.find((a) => a.kind === "text")?.text || "",
    link = work.artifacts.find((a) => a.kind === "link")?.url || "";
  function artifact(kind, value) {
    change("artifacts", [
      ...work.artifacts.filter((a) => a.kind !== kind),
      ...(value
        ? [kind === "text" ? { kind, text: value } : { kind, url: value }]
        : []),
    ]);
  }
  async function send(file = upload?.file) {
    if (!file) return;
    uploading(1);
    setError(null);
    setProgress("Checking file…");
    try {
      const asset = await uploadFile(file, upload, setUpload, setProgress);
      onChange({
        ...latest.current,
        artifacts: [
          ...latest.current.artifacts,
          { kind: "file", assetId: asset.id },
        ],
      });
      setUpload(null);
      setProgress("File uploaded and verified: " + file.name);
    } catch (e) {
      setError(e);
      setProgress("");
    } finally {
      uploading(-1);
    }
  }
  return (
    <fieldset
      className="work-editor"
      disabled={
        disabled ||
        progress === "Checking file…" ||
        progress.startsWith("Uploading")
      }
    >

      {allowSideChange && <label>
        Work type
        <select
          value={work.side}
          onChange={(e) => {
            const next = { ...work, side: e.target.value };
            if (next.side === "agent")
              next.agentConfiguration = next.agentConfiguration || {
                model: "",
              };
            else delete next.agentConfiguration;
            onChange(next);
          }}
        >
          <option value="human">Human work</option>
          <option value="agent">Agent work</option>
        </select>
      </label>}
      {missingWorkFields(work).length > 0 && <p className="small">Missing: {missingWorkFields(work).join('; ')}.</p>}
      <label className="field">
        Work title
        <input
          required
          maxLength={300}
          value={work.title}
          onChange={(e) => change("title", e.target.value)}
        />
      </label>
      <label className="field">
        The work
        <textarea
          aria-label="The work"
          rows={7}
          placeholder="Put the actual work here, or attach a file below."
          value={text}
          onChange={(e) => artifact("text", e.target.value)}
        />
      </label>
      <label className="field">
        Files
        <FilePicker
          label="Files"
          accept="image/png,image/jpeg,image/webp,application/pdf,text/plain"
          disabled={!!upload || (!!progress && progress.startsWith("Uploading"))}
          onChange={(e) => {
            if (e.target.files[0]) send(e.target.files[0]);
          }}
        />
      </label>
      <p role="status" className="small">
        {progress}
      </p>
      <ErrorNote error={error} />
      {upload && (
        <>
          <button type="button" className="text-link" onClick={() => send()}>
            Check progress & resume upload
          </button>
          <button
            type="button"
            className="text-link"
            onClick={async () => {
              try {
                await api(
                  "/me/assets/upload-sessions/" + enc(upload.sessionId),
                  { method: "DELETE" },
                );
                setUpload(null);
                setProgress("Upload cancelled. You can choose another file.");
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
      {work.artifacts
        .filter((a) => a.kind === "file")
        .map((a) => (
          <p className="small" key={a.assetId}>
            Uploaded file {a.assetId}
            <button
              type="button"
              className="text-link"
              onClick={() =>
                change(
                  "artifacts",
                  work.artifacts.filter((x) => x !== a),
                )
              }
            >
              Remove file
            </button>
          </p>
        ))}
      <label className="field">
        Supplementary link{" "}
        <span className="small">Optional; cannot replace the work</span>
        <input
          type="url"
          placeholder="https://"
          value={link}
          onChange={(e) => artifact("link", e.target.value)}
        />
      </label>
      <label className="field">
        Author / source
        <input
          required
          placeholder="Who made this, and where did it come from?"
          value={work.source}
          onChange={(e) => change("source", e.target.value)}
        />
      </label>
      {work.side === "agent" && (
        <>
          <label className="field">
            Model
            <input
              required
              placeholder="Model used to make this work"
              value={work.agentConfiguration.model}
              onChange={(e) =>
                change("agentConfiguration", {
                  ...work.agentConfiguration,
                  model: e.target.value,
                })
              }
            />
          </label>
          <details>
          <summary>Additional Agent configuration</summary>
          <label className="field">
            Prompt
            <textarea
              value={work.agentConfiguration.prompt || ""}
              onChange={(e) =>
                change("agentConfiguration", {
                  ...work.agentConfiguration,
                  prompt: e.target.value,
                })
              }
            />
          </label>
          <label className="field">
            Harness
            <input
              value={work.agentConfiguration.harness || ""}
              onChange={(e) =>
                change("agentConfiguration", {
                  ...work.agentConfiguration,
                  harness: e.target.value,
                })
              }
            />
          </label>
          </details>
        </>
      )}
      <fieldset className="permissions">
        <legend>Allow this work to be used in cloud challenges?</legend>
        <p className="small">Either choice allows you to submit for review. No permission is selected for you.</p>
        {[true, false].map((allowed) => <label key={String(allowed)}>
          <input type="radio" name="cloud" required checked={work.permissions?.cloudUse === allowed} onChange={() => {
            change("permissions", { cloudUse: allowed });
          }} />
          {allowed ? "Allow" : "Do not allow"}
        </label>)}
      </fieldset>
      {removable && (
        <button type="button" className="text-link" onClick={onRemove}>
          Remove work
        </button>
      )}
    </fieldset>
  );
}

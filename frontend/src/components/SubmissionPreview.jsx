import { useEffect, useRef } from "react";
import { apiUrl } from "../api.js";
import Artifacts from "./Artifacts";

export default function SubmissionPreview({ draft, close }) {
  const dialog = useRef();
  useEffect(() => { dialog.current.showModal(); }, []);
  return (
    <dialog className="submission-preview" ref={dialog} aria-labelledby="preview-title" onCancel={(e) => { e.preventDefault(); close(); }}>
      <div className="dialog-heading">
        <p className="eyebrow">{"Submission preview"}</p>
        <button type="button" className="close" aria-label="Close preview" onClick={close}>×</button>
      </div>
      <div className="review-preview">
        <h2 id="preview-title">{draft.title || "Untitled task"}</h2>
        <p>{draft.summary}</p>
        <div className="preview-work">
          <h3>The shared brief</h3>
          <p className="work-text">{draft.description}</p>
        </div>
        {draft.entries.map((work, index) => (
          <article className="preview-work" key={index}>
            <p className="eyebrow">{work.side} work</p>
            <h3>{work.title}</h3>
            <p className="small">{work.source}{work.agentConfiguration?.model && " / " + work.agentConfiguration.model}</p>
            <Artifacts artifacts={work.artifacts} base={apiUrl("/me")} />
            <p className="small">Cloud use: {work.permissions.cloudUse ? "allowed" : "not allowed"}</p>
          </article>
        ))}
      </div>
    </dialog>
  );
}

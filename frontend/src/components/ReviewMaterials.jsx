import { api, apiUrl, enc, id } from "../api.js";
import Artifacts from "./Artifacts.jsx";
export default function ReviewMaterials({ review }) {
  const content = review.submission.content,
    works = review.submission.kind === "task" ? content.entries : [content];
  const files = apiUrl(
    review.id ? "/admin/reviews/" + enc(review.id) : "/me",
  );
  return (
    <>
      <p className="work-text">{content.summary}</p>
      <p className="work-text">{content.description}</p>
      {content.taskAttachments?.map((a) => (
        <p key={a.assetId}>
          <a
            href={files + "/assets/" + enc(a.assetId) + "/content"}
            target="_blank"
            rel="noopener noreferrer"
          >
            Task attachment {a.assetId}
          </a>
        </p>
      ))}
      {works.map((w, i) => (
        <article className="entry-preview" key={i}>
          <p className="label">
            {w.side} · {w.source}
          </p>
          <h3>{w.title}</h3>
          <Artifacts artifacts={w.artifacts} base={files} />
          {w.agentConfiguration && (
            <>
              <p>Model: {w.agentConfiguration.model}</p>
              <p className="work-text">Prompt：{w.agentConfiguration.prompt}</p>
              <p>Harness：{w.agentConfiguration.harness}</p>
            </>
          )}
          <p>Cloud use: {w.permissions.cloudUse ? "Allow" : "Do not allow"}</p>
        </article>
      ))}
    </>
  );
}

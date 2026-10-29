import { enc, safeLink } from "../api.js";
export default function Artifacts({ artifacts = [], base }) {
  return (
    <>
      {artifacts.map((a, i) =>
        a.kind === "text" ? (
          <p className="work-text" key={i}>
            {a.text}
          </p>
        ) : a.kind === "link" ? (
          safeLink(a.url) && (
            <p key={i}>
              <a
                className="text-link"
                href={safeLink(a.url)}
                target="_blank"
                rel="noopener noreferrer"
              >
                Open supporting material ↗
              </a>
            </p>
          )
        ) : (
          <p key={i}>
            <a
              className="text-link"
              href={base + "/assets/" + enc(a.assetId) + "/content"}
              target="_blank"
              rel="noopener noreferrer"
            >
              Open work file ↗
            </a>
          </p>
        ),
      )}
    </>
  );
}

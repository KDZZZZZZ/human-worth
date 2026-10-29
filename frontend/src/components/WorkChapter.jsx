import { isCompleteWork } from "../state/models";
export default function WorkChapter({ side, works, add, edit, canAdd }) {
  return (
    <section className={"chapter " + side}>
      <div className="binding" aria-hidden="true" />
      <div className="chapter-heading">
        <p className="eyebrow">
          Chapter {side === "human" ? "I / Human work" : "II / Agent work"}
        </p>
        <h3>{side === "human" ? "Set by hand." : "Set by machine."}</h3>
        <p className="chapter-description">
          {side === "human"
            ? "Work attributed to a person. Bring your own approach to the shared task."
            : "Work attributed to an Agent. Record the model alongside each work."}
        </p>
      </div>
      <div className="work-register">
        {works.length ? (
          works.map((w, i) => (
            <div className="register-row" key={w.index}>
              <span className="register-number">
                {String(i + 1).padStart(2, "0")}
              </span>
              <div>
                <button
                  type="button"
                  className="work-title-button"
                  onClick={() => edit(w.index)}
                >
                  {w.title || "Untitled work"}
                </button>
                <p className="small">
                  {(w.artifacts.find((a) => a.kind === "text")?.text || "").trim().split(/\s+/).filter(Boolean).length} words · {isCompleteWork(w) ? "Complete locally" : "Needs attention"}
                </p>
              </div>
            </div>
          ))
        ) : (
          <div className="empty-register">
            <h4>This chapter is empty.</h4>
            <p>Add your {side} work below.</p>
            <p>A draft can wait; review needs both perspectives.</p>
          </div>
        )}
      </div>
      <div className="chapter-tools">
        {canAdd && (
          <button type="button" className="text-link add-work" onClick={add}>
            + Add {side} work
          </button>
        )}
      </div>
      <p className="chapter-readiness">
        {works.length && works.every(isCompleteWork)
          ? "✓ " + works.length + " complete in this draft"
          : "At least one complete work needed"}
      </p>
    </section>
  );
}

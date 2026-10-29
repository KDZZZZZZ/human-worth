import Artifacts from "./Artifacts";
import Report from "./Report";
export default function WorkReader({
  work,
  side,
  base,
  selected,
  choose,
  page,
  count,
}) {
  return (
    <article className="book-spread">
      <header className="book-leaf book-front">
        <p className="label">
          Chapter {side === "human" ? "I / Human Work" : "II / Agent Work"}
        </p>
        <h3>{work.title}</h3>
        <p className="small">{work.source}</p>
        <div className="work-actions">
          <button className="text-link" aria-pressed={selected} onClick={choose}>
            {selected ? "Selected · Cancel selection" : "Choose this work"}
          </button>
          <Report kind="entry" target={work.id} />
        </div>
        <p className="leaf-foot">
          Human Worth / Work
        </p>
      </header>
      <div className="book-leaf book-body" key={work.id}>
        <p className="label">The work</p>
        <div id="reader-content">
          <Artifacts artifacts={work.artifacts} base={base} />
        </div>
        <p className="leaf-foot">
          {String(page).padStart(2, "0")} / {String(count).padStart(2, "0")}
        </p>
      </div>
    </article>
  );
}

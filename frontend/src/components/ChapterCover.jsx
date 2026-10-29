export default function ChapterCover({ side, count, open }) {
  return (
    <button className="chapter-cover" data-side={side} onClick={open}>
      <span className="chapter-kicker">
        Chapter {side === "human" ? "I" : "II"}
      </span>
      <h3>{side === "human" ? "Human Work" : "Agent Work"}</h3>
      <p className="chapter-description">
        Read the approaches
        <br />
        attributed to {side === "human" ? "people" : "agents"}.
      </p>
      <span className="chapter-art" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
      <span className="chapter-bottom">
        <span>{count} {count === 1 ? "work" : "works"}</span>
        <span>{count ? "Open chapter →" : "No works supplied"}</span>
      </span>
    </button>
  );
}

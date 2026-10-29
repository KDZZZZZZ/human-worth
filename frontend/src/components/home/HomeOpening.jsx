export default function HomeOpening() {
  return (
    <section className="opening" aria-labelledby="opening-title">
      <svg
        className="geometry opening-angle"
        viewBox="0 0 520 640"
        aria-hidden="true"
      >
        <path d="M520 .75H.75V640"></path>
      </svg>
      <div className="geometry opening-board" aria-hidden="true"></div>
      <div className="geometry judgment-point" aria-hidden="true"></div>
      <h1 id="opening-title">
        <span>{"Who made it?"}</span>
        <span>{"What does it matter?"}</span>
      </h1>
      <div className="opening-copy">
        <p className="intro">
          {
            "Compare work made by people and agents, say which you prefer, and see how the gap changes over time."
          }
        </p>
        <div className="actions">
          <a className="button" href="/discovery">
            {"Browse Tasks"}
          </a>
          <a className="text-link" href="#transition">
            {"Learn More "}
            <span className="down-arrow" aria-hidden="true">
              {"↓"}
            </span>
          </a>
        </div>
      </div>
    </section>
  );
}

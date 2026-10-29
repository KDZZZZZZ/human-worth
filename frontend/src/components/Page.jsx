export default function Page({ eyebrow, title, summary, children, hero = false }) {
  return (
    <>
      {hero && (
        <div className="utility">
          <a href="/discovery">← Back to Discovery</a>
          <span className="text-link">{eyebrow}</span>
        </div>
      )}
      <header className={hero ? "task-header" : "page-heading"}>
        {hero && (
          <svg
            className="interference"
            viewBox="0 0 1750 670"
            aria-hidden="true"
          >
            <g className="hero-guides">
              <path d="M700 259H1750M1181 0V441" />
            </g>
            {[1127, 1235].flatMap((cx) =>
              [124, 177.25, 230.5, 283.75, 337, 390.25, 443.5, 496.75, 550].map(
                (r) => <circle key={cx + "-" + r} cx={cx} cy="-84" r={r} />,
              ),
            )}
          </svg>
        )}
        <div className="heading-copy">
          <p className="eyebrow">{hero ? 'TASK' : eyebrow}</p>
          <h1>{title}</h1>
        </div>
        <div className="header-note">
          <p className="label">{eyebrow}</p>
          <p>{summary}</p>
          {hero && <p className="small">Read the situation, then decide which work you would trust to guide a reader.</p>}
        </div>
      </header>
      {hero && (
        <p className="prototype-disclosure">
          {"Task materials and approved works. Your choice is a staged preference until you explicitly confirm it."}
        </p>
      )}
      {children}
    </>
  );
}

export default function SubmissionHero({ additional }) {
  return (
    <section className="hero">
      <svg className="hero-rings" viewBox="0 0 800 600" aria-hidden="true">
        <g fill="none" stroke="currentColor">
          {[85, 145, 205, 265, 325, 385, 445, 505].map((r) => (
            <circle key={r} cx="630" cy="80" r={r} />
          ))}
          <path d="M590 80h80M630 40v80" />
        </g>
      </svg>
      <p className="eyebrow">
        Submit /{" "}
        {additional ? "Another perspective" : "A new task for the record"}
      </p>
      <h1>
        {additional ? "Add a work." : "Create a task."}
        <br />
        <span>{additional ? 'Bring another perspective.' : 'Bring two perspectives.'}</span>
        <br />
        <span>{additional ? 'Join the conversation.' : 'Open it to others.'}</span>
      </h1>
      <div className="hero-bottom">
        <p>
          Add human and AI work. Your submission will be shared after review.
        </p>
        <a className="text-link" href="#task">
          Begin your submission ↓
        </a>
      </div>
    </section>
  );
}

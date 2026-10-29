export default function HomeTransition() {
  return (
    <section
      id="transition"
      className="transition"
      aria-labelledby="transition-title"
    >
      <svg className="chapter-mark" viewBox="0 0 28 28" aria-hidden="true">
        <path d="M28 .75H.75V28"></path>
      </svg>
      <h2 id="transition-title">{"The answer may depend on the task."}</h2>
      <p>
        {"Start with a specific task. Each example below is"}
        <br className="desktop-break" />
        {" a situation with works to compare."}
      </p>
    </section>
  );
}

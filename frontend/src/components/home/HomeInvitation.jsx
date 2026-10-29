export default function HomeInvitation() {
  return (
    <section className="invitation" aria-labelledby="invitation-title">
      <svg
        className="geometry invitation-angle"
        viewBox="0 0 360 360"
        aria-hidden="true"
      >
        <path d="M.75 359.25H359.25V.75M229.25 359.25A130 130 0 0 0 359.25 229.25"></path>
      </svg>
      <h2 id="invitation-title">
        {"Which task"}
        <br />
        {"matters to you?"}
      </h2>
      <p>
        {"Open any task, read the situation, and say which work you prefer."}
      </p>
      <a className="button" href="/discovery">
        {"Browse Tasks"}
      </a>
    </section>
  );
}

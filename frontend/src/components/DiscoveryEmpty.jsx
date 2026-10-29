export default function DiscoveryEmpty() {
  return (
    <section className="empty-state">
      <h2>No tasks found.</h2>
      <p>
        Try a different word or category, or reset both to explore all tasks.
      </p>
      <a className="text-link" href="/discovery">
        Reset search and category
      </a>
    </section>
  );
}

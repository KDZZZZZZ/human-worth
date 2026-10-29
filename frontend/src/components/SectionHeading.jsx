export default function SectionHeading({ number, label, title, note }) {
  return (
    <div className="section-heading">
      <span className="section-number">{number}</span>
      <div>
        <p className="eyebrow">{label}</p>
        <h2>{title}</h2>
      </div>
      <p className="section-note">{note}</p>
    </div>
  );
}

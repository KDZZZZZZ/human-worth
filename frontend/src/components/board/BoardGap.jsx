export const countValue = (value) => Number.isInteger(value) && value >= 0 ? value : <span aria-label="Data unavailable">—</span>;
export const conclusionFields = ["humanLeadingTasks", "undeterminedTasks", "agentLeadingTasks"];
export const conclusionLabels = ["Human leading", "Undetermined", "Agent leading"];

export default function BoardGap({ overall }) {
  const unavailable = ["publishedTasks", ...conclusionFields].some((field) => !Number.isInteger(overall?.[field]) || overall[field] < 0);
  return <>
    <p className="total">Published tasks <strong>{countValue(overall?.publishedTasks)}</strong></p>
    <div className="metrics">{conclusionFields.map((field, index) => <div className="metric" key={field}>
      <div className="dash">{countValue(overall?.[field])}</div><span>{conclusionLabels[index]}</span>
    </div>)}</div>
    <p className="metric-note">Task counts, not votes or ability scores.<br />{unavailable ? "Data unavailable." : overall?.publishedTasks === 0 ? "No published tasks." : "Conclusions across published tasks."}</p>
  </>;
}

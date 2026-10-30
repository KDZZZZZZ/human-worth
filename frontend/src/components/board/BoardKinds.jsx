import { countValue, conclusionFields, conclusionLabels } from "./BoardGap";

export default function BoardKinds({ categories, selectCategory }) {
  return <>
    {categories.length ? <div className="table-scroll" tabIndex={0} aria-label="Conclusion distribution by category">
      <table><thead><tr>{["Category", "Published tasks", ...conclusionLabels].map((label) => <th scope="col" key={label}>{label}</th>)}</tr></thead>
        <tbody>{categories.map(({ category, summary }) => <tr key={category ?? "null"}>
          <th scope="row">{category ? <button className="text-link" onClick={() => selectCategory(category)}>{category}</button> : "Uncategorized"}</th>
          {["publishedTasks", ...conclusionFields].map((field) => <td key={field}>{countValue(summary?.[field])}</td>)}
        </tr>)}</tbody></table>
    </div> : <p className="status">No published tasks.</p>}
    <p className="table-note">Select a category to filter Most Discussed only.<br />0 = no tasks · — = unavailable data.</p>
  </>;
}

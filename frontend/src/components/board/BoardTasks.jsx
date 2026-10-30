import { useRef, useState } from "react";
import { query } from "../../api";
import Pager from "../Pager";
import { taskPath } from "../../state/models";

export default function BoardTasks({ category, categories, selectCategory }) {
  const [shown, setShown] = useState(0);
  const ids = useRef(new Set());
  const initialLimit = Math.min(100, Math.max(3, Number(new URLSearchParams(location.search).get("shown")) || 3));
  return <>
    <label className="sr-only" htmlFor="board-category">Filter Most Discussed</label>
    <select className="filter" id="board-category" value={category} onChange={(event) => selectCategory(event.target.value, false)}>
      <option value="">All categories</option>
      {[...new Set([category, ...categories].filter(Boolean))].map((value) => <option key={value} value={value}>{value}</option>)}
    </select>
    <p className="filter-feedback" role="status">{category || "All categories"} · Showing {shown} tasks</p>
    <Pager path={"/tasks" + query({ order: "hot", category })} limit={3} initialLimit={initialLimit} moreClassName="load"
      empty="No discussed tasks in this category."
      onItems={(items) => {
        items.forEach((task) => ids.current.add(task.id));
        setShown(ids.current.size);
        const url = new URL(location.href);
        url.searchParams.set("shown", ids.current.size);
        history.replaceState(history.state, "", url);
      }}
      render={(task) => <article className="task" key={task.id}>
        <div className="task-title">{task.title}{task.category && <button className="category text-link" onClick={() => selectCategory(task.category)}>{task.category}</button>}</div>
        <p className="summary">{task.summary}</p>
        <a className="view" href={taskPath(task.id) + query({ return: location.pathname + location.search })}>View task <span>↗</span></a>
      </article>} />
  </>;
}

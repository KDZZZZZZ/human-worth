import { taskPath } from "../state/models";
export default function TaskCard({ task }) {
  return (
    <article className="task-item">
      <p className="task-category">{task.category || "Uncategorised"}</p>
      <h2>
        <a className="task-title" href={taskPath(task.id)}>
          {task.title}
        </a>
      </h2>
      <p className="task-summary">{task.summary}</p>
      <a
        className="text-link task-link"
        href={taskPath(task.id)}
        aria-label={"View Task: " + task.title}
      >
        View Task
      </a>
    </article>
  );
}

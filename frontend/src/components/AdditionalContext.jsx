import { taskPath } from "../state/models.js";
import { enc, id } from "../api.js";
import useLoad from "../hooks/useLoad.jsx";
import Load from "./Load.jsx";
export default function AdditionalContext({ taskId }) {
  const state = useLoad("/tasks/" + enc(taskId));
  return (
    <Load state={state}>
      {(task) => (
        <section className="shared-brief">
          <p className="label">Add a work</p>
          <h2>{task.title}</h2>
          <p>{task.summary}</p>
          <p className="work-text">{task.description}</p>
          <a className="text-link" href={taskPath(task.id)}>
            Back to task
          </a>
        </section>
      )}
    </Load>
  );
}

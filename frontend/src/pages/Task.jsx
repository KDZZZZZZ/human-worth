import { api, apiUrl, enc, id } from "../api.js";
import useLoad from "../hooks/useLoad.jsx";
import Load from "../components/Load.jsx";
import Page from "../components/Page.jsx";
import Report from "../components/Report.jsx";
import Judgment from "../components/Judgment.jsx";
import Comments from "../components/Comments.jsx";

export default function Task({ taskId }) {
  const state = useLoad("/tasks/" + enc(taskId));
  return (
    <Load state={state}>
      {(task) => (
        <Page
          hero
          eyebrow={task.category || "Concept task"}
          summary={task.summary}
          title={
            task.title.includes(". ") ? (
              <>
                {task.title.split(". ")[0] + "."}<br />
                {task.title.split(". ").slice(1).join(". ")}
              </>
            ) : (
              task.title
            )
          }
        >
          <section id="situation" className="section-row">
            <aside className="section-margin">
              The situation
              <Report kind="task" target={taskId} />
            </aside>
            <div className="section-body">
              <h2>Background and requirements</h2>
              <p className="lead">{task.summary}</p>
              <div className="shared-brief">
                <p className="label">Given to everyone</p>
                {task.description.split(/\n\n+/).map((text, i) => (
                  <p key={i} className="work-text brief-paragraph">
                    {text}
                  </p>
                ))}
              </div>
              <p className="small">
                Shared input reproduced as text. Supporting files, when provided, can be opened below.
              </p>
              {task.taskAttachments.map((a) => (
                <p key={a.id}>
                  <a
                    className="text-link"
                    href={apiUrl(
                      "/tasks/" +
                        enc(taskId) +
                        "/assets/" +
                        enc(a.id) +
                        "/content",
                    )}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {a.filename} ↗
                  </a>
                </p>
              ))}
            </div>
          </section>
          <Judgment taskId={taskId} />
          <Comments taskId={taskId} />
        </Page>
      )}
    </Load>
  );
}

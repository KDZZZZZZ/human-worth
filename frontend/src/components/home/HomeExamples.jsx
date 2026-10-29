import useLoad from "../../hooks/useLoad";
import Load from "../Load";
import { apiUrl, enc } from "../../api";
import { taskPath } from "../../state/models";

function FeaturedTask({ task, index }) {
  const state = useLoad('/tasks/' + enc(task.id));
  return <article className={index % 2 ? 'example example-b' : 'example example-a'}>
    <svg className="chapter-mark" viewBox="0 0 28 28" aria-hidden="true"><path d="M28 .75H.75V28" /></svg>
    <p className="example-label"><span>Published task</span><span>{task.category || 'Uncategorised'}</span></p>
    <h2><a href={taskPath(task.id)}>{task.title}</a></h2>
    <p className="background">{task.summary}</p>
    <Load state={state}>{detail => <div className={'featured-materials' + (detail.taskAttachments.some(file => file.mediaType.startsWith('image/')) ? ' has-image' : '')}>
      {detail.taskAttachments.filter(file => file.mediaType.startsWith('image/')).slice(0, 1).map(file => <figure className="library-photo" key={file.id}>
        <div className="photo-mount"><img src={apiUrl('/tasks/' + enc(task.id) + '/assets/' + enc(file.id) + '/content')} alt={file.filename} /></div>
        <figcaption className="caption">{file.filename}</figcaption>
      </figure>)}
      <aside className="document library-brief" aria-label="Common task input">
        <p className="material-label">Given to everyone</p>
        {detail.description.split(/\n\n+/).map((text, i) => <p className="work-text" key={i}>{text}</p>)}
      </aside>
    </div>}</Load>
    <div className="example-exit"><p className="works-count">Read every perspective before choosing.</p><a className="button secondary" href={taskPath(task.id)}>View Task</a></div>
  </article>;
}

export default function HomeExamples() {
  const state = useLoad('/tasks?order=recommended&limit=2');
  return <div className="context-gallery" aria-label="Featured tasks"><Load state={state}>{data => data.items.length
    ? data.items.map((task, index) => <FeaturedTask key={task.id} task={task} index={index} />)
    : <p className="state-note">No published tasks yet. <a href="/submit">Create a task</a></p>}
  </Load></div>;
}

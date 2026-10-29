import { useContext, useState } from 'react';
import { Auth } from '../state/contexts';
import { query, enc } from '../api';
import Page from '../components/Page';
import Pager from '../components/Pager';
import Login from '../components/Login';
import ErrorNote from '../components/ErrorNote';

const states = {
  task: ['draft', 'pending_review', 'rejected', 'published', 'taken_down'],
  entry: ['pending_review', 'approved', 'rejected', 'approval_revoked'],
};
export default function MySubmissions() {
  const auth = useContext(Auth);
  const [kind, setKind] = useState('task'), [state, setState] = useState('draft');
  return <Page eyebrow="Your contributions" title="My submissions" summary="Find your drafts, continue editing, and follow review decisions.">
    {auth.error ? <ErrorNote error={auth.error} retry={auth.reload} /> : !auth.data ? <Login /> : <>
      <div className="submission-filters">
        <div><label htmlFor="submission-kind">Submission type</label><select id="submission-kind" value={kind} onChange={event => { const next = event.target.value; setKind(next); setState(states[next][0]); }}><option value="task">Tasks</option><option value="entry">Additional works</option></select></div>
        <div><label htmlFor="submission-state">Status</label><select id="submission-state" value={state} onChange={event => setState(event.target.value)}>{states[kind].map(value => <option key={value} value={value}>{value.replaceAll('_', ' ')}</option>)}</select></div>
        <a className="button" href="/submit">Create a task</a>
      </div>
      <Pager key={kind + state} path={'/me/submissions' + query({ kind, state })} empty="No submissions with this status." render={item => <article className="task-item" key={item.id}>
        <p className="task-category">{item.state.replaceAll('_', ' ')} · Revision {item.revision}</p>
        <h2>{item.content.title}</h2><p>{item.content.summary || item.content.source}</p>
        {item.rejectionReason && <p role="note">Reviewer note: {item.rejectionReason}</p>}
        <a className="text-link" href={'/submit?' + (kind === 'task' ? 'edit=' : 'entry=') + enc(item.id)}>{['draft', 'rejected'].includes(item.state) ? 'Continue editing' : 'View submission'}</a>
        {item.state === 'published' && <a className="text-link" href={'/tasks/' + enc(item.id)}>View published task</a>}
      </article>} />
    </>}
  </Page>;
}

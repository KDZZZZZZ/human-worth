import useLoad from '../hooks/useLoad';
import Load from './Load';

export default function BoardSummary() {
  const state = useLoad('/board');
  return <Load state={state}>{board => <>
    <p>Conclusions across published tasks. These totals are task counts, not individual votes.</p>
    <dl>
      <div><dt>Published tasks</dt><dd>{board.overall.publishedTasks}</dd></div>
      <div><dt>Human leading</dt><dd>{board.overall.humanLeadingTasks}</dd></div>
      <div><dt>Agent leading</dt><dd>{board.overall.agentLeadingTasks}</dd></div>
      <div><dt>Undetermined</dt><dd>{board.overall.undeterminedTasks}</dd></div>
    </dl>
    <a className="text-link" href="/discovery?order=hot">Explore popular tasks →</a>
  </>}</Load>;
}

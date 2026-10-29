import { useContext, useState } from 'react';
import { Auth } from '../state/contexts';
import { api } from '../api';
import { designRecords, importDesign } from '../../scripts/data/import-design';
import ErrorNote from '../components/ErrorNote';
import Login from '../components/Login';

export default function ImportDesign() {
  const auth = useContext(Auth);
  const [busy, setBusy] = useState(false), [message, setMessage] = useState(''), [error, setError] = useState(null), [result, setResult] = useState([]);
  async function run() {
    setBusy(true); setError(null);
    try { setResult(await importDesign({ api, accountId: auth.data.account.id, progress: setMessage })); setMessage('Import complete. Data was saved through the live API.'); }
    catch (error) { setError(error); }
    finally { setBusy(false); }
  }
  return <section className="page-state">
    <h1>Import design data</h1>
    <p>Save the original design content through the live API. The library task includes four works and will be reviewed and published. Seven incomplete tasks and the Submit example remain drafts. Six example comments are attributed to your actual account. No votes or accounts are fabricated.</p>
    <ul>{designRecords().map(record => <li key={record.key}>{record.content.title} — {record.publish ? '4 works · submit and approve' : 'save as draft'}</li>)}</ul>
    {!auth.data ? <Login /> : auth.data.role !== 'admin' ? <p>An administrator account is required to approve the imported task.</p> : <button className="button" disabled={busy} onClick={run}>{busy ? 'Importing…' : 'Import design data via API'}</button>}
    <ErrorNote error={auth.error || error} /><p role="status">{message}</p>
    <ul>{result.map(item => <li key={item.id}><a href={item.state === 'published' ? '/tasks/' + item.id : '/submit?edit=' + item.id}>{item.title} — {item.state}</a></li>)}</ul>
    <p><a href="/discovery">Open Discovery</a></p>
  </section>;
}

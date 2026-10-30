import { useRef, useState } from 'react';
import { configurationError } from './api';
import { ConfirmationDialog, DataTable, DetailPanel, FormField, Loading, Notice, Pagination, ProgressIndicator, useAdminLoad } from './ui';

const steps = ['Select task', 'Initial package', 'Packer', 'Executor', 'Ranker', 'Limits', 'Review & launch'];
const initial = () => ({ initialTaskPackage: { taskRevision: 1, taskAttachmentIds: [], description: '', workRequirements: '' }, packer: { model: '', prompt: '', parameters: {} }, executor: { model: '', prompt: '', harness: '', skills: [], parameters: {} }, ranker: { model: '', prompt: '', parameters: {} }, rankerFitThreshold: '', rankerMinComparablePairs: '', rankerRoundLimit: '', roundLimit: '', budget: { amount: '', unit: 'model_calls' } });
function TaskPicker({ client, choose, selected }) {
  const [history, setHistory] = useState([]); const state = useAdminLoad(() => client.tasks({ cursor: history.at(-1) }), [history]);
  return <Loading state={state}>{d => <><DataTable rows={d.items} columns={[{ title: 'Published task', render: t => <><strong>{t.title}</strong><small>{t.id}</small></> }, { title: 'Revision', render: t => t.revision }, { title: 'Select', render: t => <button aria-pressed={selected?.id === t.id} onClick={() => choose(t)}>{selected?.id === t.id ? 'Selected' : 'Select task'}</button> }]} /><Pagination nextCursor={d.nextCursor} history={history} setHistory={setHistory} /></>}</Loading>;
}
function RoleFields({ role, value, change, json, setJson }) {
  return <div className="ad-form-grid"><FormField label="Model ID" hint="Use a model identifier configured on the server; no provider URLs or API keys."><input required value={value.model} onChange={e => change({ ...value, model: e.target.value })} /></FormField><FormField label="Prompt"><textarea rows={6} value={value.prompt || ''} onChange={e => change({ ...value, prompt: e.target.value })} /></FormField>{role === 'executor' && <><FormField label="Harness"><input value={value.harness || ''} onChange={e => change({ ...value, harness: e.target.value })} /></FormField><FormField label="Skills" hint="One configured skill identifier per line."><textarea rows={3} value={(value.skills || []).join('\n')} onChange={e => change({ ...value, skills: e.target.value.split('\n') })} /></FormField></>}<FormField label="Parameters (JSON)" hint={role === 'executor' ? 'Provider-specific parameters are checked by the server. Do not enter secrets.' : 'Only temperature (0–2) and top_p (>0–1) are accepted.'}><textarea rows={4} spellCheck="false" value={json} onChange={e => setJson(e.target.value)} /></FormField></div>;
}
export default function CreateChallenge({ client, link }) {
  const parentId = new URLSearchParams(location.search).get('restart');
  const state = useAdminLoad(async () => parentId ? client.run(parentId) : null, [parentId]);
  return <Loading state={state}>{parent => parent && !['completed', 'failed', 'cancelled'].includes(parent.status) ? <Notice danger>Only completed, failed or cancelled runs can be restarted.</Notice> : <Wizard client={client} link={link} parent={parent} />}</Loading>;
}
function Wizard({ client, link, parent }) {
  const [step, setStep] = useState(0), [config, setConfig] = useState(() => parent ? structuredClone(parent.configuration) : initial()), [task, setTask] = useState(null), [json, setJson] = useState(() => Object.fromEntries(['packer', 'executor', 'ranker'].map(k => [k, JSON.stringify(parent?.configuration[k]?.parameters || {}, null, 2)]))), [error, setError] = useState(''), [confirm, setConfirm] = useState(false), [busy, setBusy] = useState(false);
  const request = useRef(null);
  function update(next) { setConfig(next); request.current = null; }
  function materialize() {
    const next = structuredClone(config);
    for (const role of ['packer', 'executor', 'ranker']) { try { next[role].parameters = JSON.parse(json[role]); } catch { throw Error(`Invalid ${role} parameters JSON.`); } }
    next.executor.skills = (next.executor.skills || []).filter(s => s.trim());
    return next;
  }
  async function choose(t) {
    setBusy(true); setError('');
    try { const detail = await client.task(t.id); setTask(detail); update({ ...config, initialTaskPackage: { ...config.initialTaskPackage, taskRevision: detail.revision, taskAttachmentIds: [] } }); }
    catch (e) { setError(e.message); } finally { setBusy(false); }
  }
  function next() {
    setError('');
    if (!task) { setError('Select a published task first.'); return; }
    if (parent && task.id !== parent.taskId) { setError('A restart must use the original task.'); return; }
    if (step === 1 && (!config.initialTaskPackage.description.trim() || !config.initialTaskPackage.workRequirements.trim())) { setError('Enter the execution description and work requirements.'); return; }
    if ([2, 3, 4].includes(step)) {
      const role = ['packer', 'executor', 'ranker'][step - 2];
      if (!config[role].model.trim()) { setError('Enter a configured model ID.'); return; }
      try { const parsed = JSON.parse(json[role]); if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw Error(); } catch { setError('Parameters must be a JSON object.'); return; }
    }
    if (step === 5) { try { const problem = configurationError(materialize()); if (problem) throw Error(problem); } catch (e) { setError(e.message); return; } }
    setStep(step + 1);
  }
  async function launch() {
    setBusy(true); setError('');
    try {
      // Keep the exact request and key across timeout retries; editing invalidates both.
      if (!request.current) { const c = materialize(), problem = configurationError(c); if (problem) throw Error(problem); request.current = { configuration: c, key: crypto.randomUUID() }; }
      const r = parent ? await client.restart(parent.id, request.current.configuration, request.current.key) : await client.start({ taskId: task.id, configuration: request.current.configuration }, request.current.key);
      location.assign(link('runs/' + r.id));
    } catch (e) { setError(e.message); } finally { setBusy(false); }
  }
  return <><a className="ad-back" href={link('runs')}>← Challenge runs</a><DetailPanel title={parent ? 'Restart challenge' : 'Create challenge'}><ProgressIndicator steps={steps} current={step} />{parent && <Notice>A new run will link to {parent.id}. Select the original task ({parent.taskId}) to revalidate its current revision. The previous record stays unchanged.</Notice>}<h3>{steps[step]}</h3>
    {step === 0 && <TaskPicker client={client} choose={choose} selected={task} />}
    {step === 1 && <div className="ad-form-grid"><p><strong>{task.title}</strong> · Revision {task.revision}</p><Notice>The public task API does not expose cloud-use grants. This form selects no attachments; text-only packages are supported. Permission-aware attachment selection requires a backend projection.</Notice><FormField label="Execution description"><textarea rows={5} value={config.initialTaskPackage.description} onChange={e => update({ ...config, initialTaskPackage: { ...config.initialTaskPackage, description: e.target.value } })} /></FormField><FormField label="Work requirements"><textarea rows={4} value={config.initialTaskPackage.workRequirements} onChange={e => update({ ...config, initialTaskPackage: { ...config.initialTaskPackage, workRequirements: e.target.value } })} /></FormField></div>}
    {[2, 3, 4].includes(step) && (() => { const role = ['packer', 'executor', 'ranker'][step - 2]; return <RoleFields role={role} value={config[role]} change={v => update({ ...config, [role]: v })} json={json[role]} setJson={v => { setJson({ ...json, [role]: v }); request.current = null; }} />; })()}
    {step === 5 && <div className="ad-form-grid ad-two-columns">{[['Ranker fit threshold', 'rankerFitThreshold', 0.000001, 0.999999, 'any'], ['Minimum comparable pairs', 'rankerMinComparablePairs', 1, undefined, 1], ['Ranker round limit', 'rankerRoundLimit', 1, 100, 1], ['Packer / executor round limit', 'roundLimit', 1, 100, 1]].map(([name, k, min, max, inc]) => <FormField key={k} label={name}><input type="number" required min={min} max={max} step={inc} value={config[k]} onChange={e => update({ ...config, [k]: e.target.value === '' ? '' : Number(e.target.value) })} /></FormField>)}<FormField label="Budget amount" hint="Explicit execution limit, not a balance or usage figure."><input type="number" required min="1" max="100000" step="1" value={config.budget.amount} onChange={e => update({ ...config, budget: { amount: e.target.value === '' ? '' : Number(e.target.value), unit: 'model_calls' } })} /></FormField><FormField label="Budget unit"><input readOnly value="model_calls" /></FormField></div>}
    {step === 6 && <><Notice>The backend rechecks task revision, materials, model configuration and authorization. Launch creates a queued run; it does not imply a successful result.</Notice><pre>{JSON.stringify({ taskId: task.id, configuration: materialize() }, null, 2)}</pre></>}
    {error && !confirm && <Notice danger>{error}</Notice>}<div className="ad-wizard-footer"><span>Step {step + 1} of {steps.length}</span><div className="ad-actions"><button disabled={step === 0 || busy} onClick={() => { setStep(step - 1); setError(''); }}>Back</button>{step < 6 ? <button disabled={busy} className="ad-primary" onClick={next}>{busy ? 'Loading task…' : 'Continue'}</button> : <button className="ad-primary" onClick={() => { setConfirm(true); setError(''); }}>{parent ? 'Restart challenge' : 'Launch challenge'}</button>}</div></div></DetailPanel>
    {confirm && <ConfirmationDialog title={parent ? 'Create a new linked run?' : 'Launch this challenge?'} confirm={launch} cancel={() => setConfirm(false)} busy={busy} error={error}><p>{task.title}</p><p>This starts the configured R calibration, then P/E optimization only when the fit threshold is exceeded. Candidates still require review.</p></ConfirmationDialog>}</>;
}

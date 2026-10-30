import { useEffect, useRef, useState } from 'react';

export function useAdminLoad(load, deps = []) {
  const [state, set] = useState({ loading: true }), [version, bump] = useState(0);
  useEffect(() => {
    let current = true;
    set({ loading: true });
    Promise.resolve().then(load).then(data => { if (current) set({ data }); }, error => { if (current) set({ error }); });
    return () => { current = false; };
  }, [...deps, version]);
  return { ...state, reload: () => bump(n => n + 1) };
}
export const time = v => v ? new Date(v).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '—';
export const label = v => String(v || '—').replaceAll('_', ' ');
const tones = { approved: 'success', applied: 'success', completed: 'success', passed: 'success', succeeded: 'success', registered: 'success', dismissed: 'neutral', rejected: 'danger', failed: 'danger', blocked: 'danger', invalidated: 'danger', actioned: 'danger', active: 'processing', queued: 'processing', finalizing: 'processing', pending: 'warning', pending_review: 'warning', open: 'warning', cancelling: 'warning', not_met: 'warning', insufficient_data: 'warning' };
export function StatusBadge({ value }) { return <span className={'ad-badge ad-' + (tones[value] || 'neutral')}><i />{label(value)}</span>; }
export function Notice({ children, danger = false }) { return <p className={'ad-notice' + (danger ? ' ad-error' : '')} role={danger ? 'alert' : 'status'}>{children}</p>; }
export function Loading({ state, children }) {
  if (state.loading) return <p className="ad-loading" role="status">Loading…</p>;
  if (state.error) return <Notice danger>{state.error.message} <button onClick={state.reload}>Retry</button></Notice>;
  return children(state.data);
}
export function DetailPanel({ title, children, action, className = '' }) { return <section className={'ad-panel ' + className}><div className="ad-panel-heading"><h2>{title}</h2>{action}</div>{children}</section>; }
export function DataTable({ columns, rows, empty = 'No records in this view.' }) {
  return rows.length ? <div className="ad-table-wrap" tabIndex="0" aria-label="Scrollable records"><table><thead><tr>{columns.map(c => <th key={c.title} scope="col">{c.title}</th>)}</tr></thead><tbody>{rows.map((row, i) => <tr key={row.id || i}>{columns.map(c => <td key={c.title}>{c.render(row)}</td>)}</tr>)}</tbody></table></div> : <div className="ad-empty"><span aria-hidden="true">✓</span><p>{empty}</p></div>;
}
export function Pagination({ nextCursor, history, setHistory }) { return <div className="ad-pagination"><span>Page {history.length + 1}</span><button disabled={!history.length} onClick={() => setHistory(history.slice(0, -1))}>Previous</button><button disabled={!nextCursor} onClick={() => setHistory([...history, nextCursor])}>Next</button></div>; }
export function FilterBar({ children }) { return <div className="ad-filters">{children}</div>; }
export function FormField({ label: name, hint, children }) { return <label className="ad-field"><span>{name}</span>{children}{hint && <small>{hint}</small>}</label>; }
export function ProgressIndicator({ steps, current }) { return <ol className="ad-steps">{steps.map((name, i) => <li key={name} aria-current={i === current ? 'step' : undefined} className={i < current ? 'complete' : ''}><span>{i < current ? '✓' : i + 1}</span>{name}</li>)}</ol>; }
export function ConfirmationDialog({ title, children, confirm, cancel, busy, error }) {
  const ref = useRef();
  useEffect(() => { const d = ref.current; d.showModal(); return () => d.close(); }, []);
  return <dialog className="ad-dialog" ref={ref} onCancel={e => { e.preventDefault(); if (!busy) cancel(); }}><h2>{title}</h2><div>{children}</div>{error && <Notice danger>{error}</Notice>}<div className="ad-actions"><button disabled={busy} onClick={cancel}>Go back</button><button className="ad-primary" disabled={busy} onClick={confirm}>{busy ? 'Submitting…' : 'Confirm'}</button></div></dialog>;
}
export function MetricBlock({ name, value, texture, href, index }) { return <a className={'ad-metric texture-' + texture} style={{ '--metric-level': index }} href={href}><span>{name}</span><strong>{value}</strong><small>View records <span aria-hidden="true">↗</span></small></a>; }
export function KeyValues({ values }) { return <dl className="ad-key-values">{values.map(([k, v]) => <div key={k}><dt>{k}</dt><dd>{v ?? '—'}</dd></div>)}</dl>; }

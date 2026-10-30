import { useContext, useEffect, useState } from 'react';
import { Auth } from '../state/contexts';
import { api, apiUrl, setSession } from '../api';
import { liveAdmin } from './api';
import { Overview, Reviews, ReviewDetail, Reports, Runs, RunDetail, AuditLog } from './pages';
import CreateChallenge from './CreateChallenge';
import { Loading, Notice, useAdminLoad } from './ui';
import './admin.css';

const groups = [{ name: 'Workspace', items: [['', 'Overview', 'overview'], ['reviews', 'Reviews', 'reviews'], ['reports', 'Reports', 'reports']] }, { name: 'Operations', items: [['runs', 'Challenge runs', 'runs'], ['audit', 'Audit log', 'audit']] }];
function Icon({ type }) {
  const paths = { overview: 'M3 3h7v7H3z M14 3h7v7h-7z M3 14h7v7H3z M14 14h7v7h-7z', reviews: 'M7 3h10v3H7z M6 5H4v16h16V5h-2 M8 12l3 3 5-6', reports: 'M5 22V3 M5 3h14l-3 5 3 5H5', runs: 'M4 6h16 M4 12h16 M4 18h16 M8 3v6 M16 9v6 M10 15v6', audit: 'M5 3h14v18H5z M8 7h8 M8 11h8 M8 15h5' };
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true"><path d={paths[type]} /></svg>;
}
export default function AdminConsole() {
  const auth = useContext(Auth), [client, setClient] = useState(null), [error, setError] = useState(null);
  const preview = import.meta.env.DEV && /^\/admin-preview(?:\/|$)/.test(location.pathname);
  useEffect(() => {
    let active = true;
    if (preview && import.meta.env.DEV) import('./demo').then(m => { if (active) setClient(m.demoAdmin); }).catch(e => { if (active) setError(e); });
    else setClient(liveAdmin);
    return () => { active = false; };
  }, [preview]);
  if (error) return <div className="ad-root ad-gate"><Notice danger>{error.message}</Notice></div>;
  if (!client) return <div className="ad-root ad-gate" role="status">Loading console…</div>;
  if (preview) return <AdminLayout client={client} preview account={{ id: 'admin-demo', displayName: 'Demo administrator' }} />;
  return <LiveGate auth={auth} client={client} />;
}
function LiveGate({ auth, client }) {
  // Revalidate independently: SessionProvider may initially contain a cached role.
  const session = useAdminLoad(() => api('/me'), [auth.data?.account?.id]);
  useEffect(() => { if (session.data) setSession(session.data); }, [session.data]);
  if (session.loading) return <div className="ad-root ad-gate" role="status">Verifying administrator access…</div>;
  if (session.error) return <div className="ad-root ad-gate"><h1>Admin access</h1>{session.error.status === 401 ? <><p>Sign in with an administrator account.</p><a className="ad-primary" href={apiUrl('/auth/google')}>Sign in with Google</a></> : <Notice danger>{session.error.message} <button onClick={session.reload}>Retry</button></Notice>}<a href="/">Return to website</a></div>;
  if (session.data?.role !== 'admin') return <div className="ad-root ad-gate"><h1>Administrator access required</h1><p>Your account cannot access management tools.</p><a href="/">Return to website</a></div>;
  return <AdminLayout client={client} account={session.data.account} />;
}
function AdminSidebar({ link, active }) { return <aside className="ad-sidebar"><a className="ad-brand" href={link('')}>Human Worth<span>Admin console</span></a><nav aria-label="Admin navigation">{groups.map(g => <div className="ad-nav-group" key={g.name}><p>{g.name}</p>{g.items.map(([path, name, icon]) => <a key={path} href={link(path)} aria-current={active === path ? 'page' : undefined}><Icon type={icon} />{name}</a>)}</div>)}</nav><a className="ad-site-link" href="/">↗ Public website</a><p className="ad-sidebar-note">Human judgment.<br />Careful stewardship.</p></aside>; }
function AdminHeader({ account, preview, client }) {
  const [error, setError] = useState(''), [busy, setBusy] = useState(false);
  async function logout() { setBusy(true); try { await api('/auth/logout', { method: 'POST' }); setSession(null); location.assign('/'); } catch (e) { setError(e.message); } finally { setBusy(false); } }
  return <><header className="ad-header"><span>Human Worth Admin</span><div><span className="ad-avatar" aria-hidden="true">{account.displayName?.slice(0, 1) || 'A'}</span><span>{account.displayName || account.id}<small>{preview ? 'Local preview identity' : account.id}</small></span>{preview ? <button onClick={() => { client.reset(); location.reload(); }}>Reset demo</button> : <button disabled={busy} onClick={logout}>{busy ? 'Signing out…' : 'Sign out'}</button>}</div></header>{error && <Notice danger>{error}</Notice>}</>;
}
function AdminLayout({ client, preview = false, account }) {
  const base = preview ? '/admin-preview' : '/admin', path = location.pathname.slice(base.length).replace(/^\/+|\/+$/g, ''), parts = path.split('/'), active = parts[0] || '';
  const link = p => base + (p ? '/' + p : '');
  const headings = { '': ['Overview', 'Your workspace, at a glance.'], reviews: ['Reviews', 'Read the work. Make the call.'], reports: ['Reports', 'Resolve concerns with a clear record.'], runs: ['Challenge runs', 'Follow calibration, execution and review.'], audit: ['Audit log', 'A traceable record of administrative decisions.'] };
  const [title, subtitle] = headings[active] || ['Page not found', 'Choose a section from the sidebar.'];
  let page;
  if (!path) page = <Overview client={client} link={link} />;
  else if (path === 'reviews') page = <Reviews client={client} link={link} />;
  else if (parts[0] === 'reviews' && parts.length === 2) page = <ReviewDetail client={client} link={link} id={decodeURIComponent(parts[1])} />;
  else if (path === 'reports') page = <Reports client={client} />;
  else if (path === 'runs') page = <Runs client={client} link={link} />;
  else if (path === 'runs/new') page = <CreateChallenge client={client} link={link} />;
  else if (parts[0] === 'runs' && parts.length === 2) page = <RunDetail client={client} link={link} id={decodeURIComponent(parts[1])} />;
  else if (path === 'audit') page = <AuditLog client={client} />;
  else page = <Notice>Page not found. <a href={link('')}>Open overview</a></Notice>;
  return <div className="ad-root"><a className="ad-skip" href="#admin-main">Skip to content</a><AdminSidebar link={link} active={active} /><div className="ad-shell"><AdminHeader account={account} preview={preview} client={client} />{preview && <div className="ad-demo-banner"><strong>LOCAL DEMO</strong><span>All records and operations are simulated. No API writes, real reviews or model calls. Refresh completes simulated pending actions.</span></div>}<main id="admin-main" className="ad-main"><div className="ad-page-heading"><div><p>{preview ? 'Preview workspace' : 'Administration'}</p><h1>{title}</h1><span>{subtitle}</span></div><span className="ad-identity-mark" aria-hidden="true">HW</span></div>{page}</main><footer className="ad-footer">Human Worth · Administration<span>{preview ? 'Isolated demo fixtures' : 'Server-authorized operations'}</span></footer></div></div>;
}

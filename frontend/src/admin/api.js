import { api, apiUrl, enc, query } from '../api';

// Only parameters declared by openapi.yaml are sent to the service.
export const liveAdmin = {
  dashboard: () => api('/admin/dashboard'),
  reviews: ({ status = 'pending', cursor, limit = 10 } = {}) => api('/admin/reviews' + query({ status, cursor, limit })),
  review: id => api('/admin/reviews/' + enc(id)),
  decide: (id, body) => api('/admin/reviews/' + enc(id) + '/decision', { method: 'POST', body }),
  asset: (reviewId, assetId) => apiUrl('/admin/reviews/' + enc(reviewId) + '/assets/' + enc(assetId) + '/content'),
  reports: ({ status = 'open', cursor, limit = 10 } = {}) => api('/admin/reports' + query({ status, cursor, limit })),
  resolve: (id, body) => api('/admin/reports/' + enc(id) + '/resolution', { method: 'POST', body }),
  runs: ({ cursor, limit = 10 } = {}) => api('/admin/challenge-runs' + query({ cursor, limit })),
  run: id => api('/admin/challenge-runs/' + enc(id)),
  cancel: (id, reason) => api('/admin/challenge-runs/' + enc(id) + '/cancel', { method: 'POST', body: { reason } }),
  start: (body, key) => api('/admin/challenge-runs', { method: 'POST', body, headers: { 'Idempotency-Key': key } }),
  restart: (id, body, key) => api('/admin/challenge-runs/' + enc(id) + '/restart', { method: 'POST', body, headers: { 'Idempotency-Key': key } }),
  register: (id, candidate) => api('/admin/challenge-runs/' + enc(id) + '/candidates/' + enc(candidate) + '/registration', { method: 'PUT' }),
  audit: ({ cursor, limit = 10 } = {}) => api('/admin/audit-events' + query({ cursor, limit })),
  tasks: ({ cursor, limit = 10 } = {}) => api('/tasks' + query({ cursor, limit })),
  task: id => api('/tasks/' + enc(id)),
};

export function reasonError(reason, required = true) {
  if (required && !reason.trim()) return 'Enter a reason before continuing.';
  if (reason.includes('\0') || new TextEncoder().encode(reason).length > 4000) return 'Reason must contain no NUL characters and fit within 4,000 UTF-8 bytes.';
  return '';
}

export function configurationError(c) {
  const p = c.initialTaskPackage;
  if (!p || !Number.isInteger(p.taskRevision) || p.taskRevision < 1 || !p.description?.trim() || !p.workRequirements?.trim()) return 'Complete the task package, revision and work requirements.';
  if (!Array.isArray(p.taskAttachmentIds) || new Set(p.taskAttachmentIds).size !== p.taskAttachmentIds.length) return 'Task attachment IDs must be unique.';
  for (const role of ['packer', 'executor', 'ranker']) {
    if (!c[role]?.model?.trim()) return `Enter a server-configured ${role} model ID.`;
    const params = c[role].parameters;
    if (params && (typeof params !== 'object' || Array.isArray(params))) return `${role} parameters must be a JSON object.`;
    if (role !== 'executor') for (const [k, v] of Object.entries(params || {})) {
      if (!['temperature', 'top_p'].includes(k)) return `${role} only accepts temperature and top_p parameters.`;
      if (typeof v !== 'number' || !Number.isFinite(v) || (k === 'temperature' ? v < 0 || v > 2 : v <= 0 || v > 1)) return `Invalid ${role} ${k}.`;
    }
  }
  if (!(c.rankerFitThreshold > 0 && c.rankerFitThreshold < 1)) return 'Ranker fit threshold must be strictly between 0 and 1.';
  for (const [k, max] of [['rankerMinComparablePairs', Infinity], ['rankerRoundLimit', 100], ['roundLimit', 100]]) if (!Number.isInteger(c[k]) || c[k] < 1 || c[k] > max) return `Invalid ${k}.`;
  if (!Number.isInteger(c.budget?.amount) || c.budget.amount < 1 || c.budget.amount > 100000 || c.budget.unit !== 'model_calls') return 'Set a budget of 1–100,000 model_calls.';
  return '';
}

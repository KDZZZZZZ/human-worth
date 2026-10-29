import data from './design-data.json' with { type: 'json' };

export function designRecords() {
  const tasks = data.tasks.map(({ id, ...content }) => ({
    key: id, publish: !!data.worksByTask[id]?.length,
    content: { ...content, taskAttachments: [], entries: (data.worksByTask[id] || []).map(work => ({
      side: work.side, title: work.title, source: work.source,
      artifacts: [{ kind: 'text', text: work.text }], permissions: { cloudUse: false },
      ...(work.side === 'agent' ? { agentConfiguration: { model: 'Illustrative example — no model run' } } : {}),
    })) },
  }));
  const draft = data.submission;
  tasks.push({ key: 'submission-example', publish: false, content: {
    title: draft.title, summary: draft.summary, description: draft.description, category: draft.category, taskAttachments: [],
    entries: ['human', 'agent'].flatMap(side => draft.works[side].map(work => ({
      side, title: work.title, source: work.source, artifacts: [{ kind: 'text', text: work.body }], permissions: { cloudUse: false },
      ...(side === 'agent' ? { agentConfiguration: { model: work.model, ...(work.prompt ? { prompt: work.prompt } : {}), ...(work.harness ? { harness: work.harness } : {}) } } : {}),
    }))),
  } });
  return tasks;
}

// Only normal authenticated HTTP operations; never direct SQL or synthetic users/votes.
// The journal holds IDs/idempotency keys, never credentials or invented business state.
function matchesInput(actual, expected) {
  if (Array.isArray(expected)) return Array.isArray(actual) && actual.length === expected.length && expected.every((value, index) => matchesInput(actual[index], value));
  if (expected && typeof expected === 'object') return actual && Object.entries(expected).every(([key, value]) => matchesInput(actual[key], value));
  return actual === expected;
}
export async function importDesign(options) {
  if (globalThis.navigator?.locks) return navigator.locks.request('human-worth:design-import:' + options.accountId, () => performImport(options));
  return performImport(options);
}
async function performImport({ api, accountId, storage = localStorage, progress = () => {} }) {
  const session = await api('/me');
  if (session.role !== 'admin' || session.account.id !== accountId) throw Error('Sign in with the administrator account selected for this import.');
  const key = 'human-worth-design-import-v1:' + accountId;
  const journal = JSON.parse(storage.getItem(key) || '{}');
  const save = () => storage.setItem(key, JSON.stringify(journal));
  const result = [];
  for (const record of designRecords()) {
    const item = journal[record.key] ||= { key: crypto.randomUUID() };
    save();
    progress('Saving ' + record.content.title);
    let draft = item.id
      ? await api('/me/tasks/' + encodeURIComponent(item.id))
      : await api('/tasks', { method: 'POST', body: record.content, headers: { 'Idempotency-Key': item.key } });
    item.id = draft.id;
    save();
    if (!matchesInput(draft.content, record.content)) throw Error('The saved draft was edited. Inspect it before resuming this import.');
    if (record.publish && draft.state === 'draft') {
      draft = await api('/me/tasks/' + encodeURIComponent(draft.id) + '/review-requests', { method: 'POST', body: { expectedRevision: draft.revision } });
    }
    if (record.publish && draft.state === 'pending_review') {
      if (!draft.reviewId) throw Error('Review ID is not available for ' + draft.id);
      const reviewPath = '/admin/reviews/' + encodeURIComponent(draft.reviewId);
      const review = await api(reviewPath);
      if (!matchesInput(review.submission.content, record.content)) throw Error('Review snapshot changed. Inspect it before approving.');
      if (review.status === 'pending' && !review.decision) {
        await api(reviewPath + '/decision', { method: 'POST', body: { decision: 'approve', reason: 'Design reference data imported at the project owner’s request. Fictional example works; no actual model run.' } });
      }
      let applied = false;
      for (let attempt = 0; attempt < 20; attempt++) {
        const current = await api(reviewPath);
        if (current.deliveryState === 'blocked' || current.status === 'rejected') throw Error('Review needs attention: ' + current.id);
        if (current.status === 'approved' && current.deliveryState === 'applied') { applied = true; break; }
        await new Promise(resolve => setTimeout(resolve, 1000));
      }
      if (!applied) throw Error('Review is still being applied. Resume import later.');
      draft = await api('/me/tasks/' + encodeURIComponent(draft.id));
    }
    if (record.publish && draft.state !== 'published') throw Error('Task is not published: ' + draft.state);
    if (record.publish) {
      const publicTask = await api('/tasks/' + encodeURIComponent(draft.id));
      if (publicTask.title !== record.content.title) throw Error('Published title differs from design input.');
      for (const [index, comment] of data.comments.filter(c => c.taskId === record.key).entries()) {
        const commentItem = (item.comments ||= [])[index] ||= { key: crypto.randomUUID() };
        save();
        if (!commentItem.id) {
          const saved = await api('/tasks/' + encodeURIComponent(draft.id) + '/comments', { method: 'POST', body: { body: comment.body }, headers: { 'Idempotency-Key': commentItem.key } });
          commentItem.id = saved.id;
          save();
        }
      }
    }
    result.push({ key: record.key, id: draft.id, title: draft.content.title, state: draft.state });
    progress('Saved: ' + draft.content.title + ' (' + draft.state + ')');
  }
  return result;
}

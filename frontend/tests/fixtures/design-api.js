import data from "../../scripts/data/design-data.json" with { type: "json" };
const clone = (value) => structuredClone(value);
const key = "human-worth-design-preview-v1";
const user = { id: "preview-reader", displayName: "Example reader" };
const session = { account: user, role: "user", csrfToken: "preview-only" };
const fresh = () => ({
  signed: true,
  votes: {},
  closed: {},
  comments: clone(data.comments),
  drafts: {},
  entries: {},
});
let memory;
export async function mockDesignApi(page) {
  memory = fresh();
  memory.drafts['fixture-submission'] = { id: 'fixture-submission', kind: 'task', state: 'draft', revision: 1, content: designDraft(), reviewId: null };
  await page.route('**/api/**', async route => {
    const request = route.request(), url = new URL(request.url());
    try {
      const result = await previewApi(url.pathname.slice(4) + url.search, { method: request.method(), body: request.postData() ? request.postDataJSON() : undefined });
      await route.fulfill({ status: result === null ? 204 : 200, contentType: 'application/json', body: result === null ? undefined : JSON.stringify(result) });
    } catch (error) {
      await route.fulfill({ status: error.status || 500, contentType: 'application/json', body: JSON.stringify({ detail: error.message }) });
    }
  });
}
function state() {
  if (memory) return memory;
  try {
    memory = JSON.parse(sessionStorage.getItem(key)) || fresh();
  } catch {
    memory = fresh();
  }
  return memory;
}
function persist() {
  try {
    sessionStorage.setItem(key, JSON.stringify(state()));
  } catch {}
}
export function designDraft() {
  const d = data.submission;
  return {
    title: d.title,
    summary: d.summary,
    description: d.description,
    category: d.category,
    taskAttachments: [],
    entries: ["human", "agent"].flatMap((side) =>
      d.works[side].map((w) => ({
        side,
        title: w.title,
        source: w.source,
        artifacts: [{ kind: "text", text: w.body }],
        permissions: { cloudUse: w.cloud === "yes" },
        ...(side === "agent"
          ? {
              agentConfiguration: {
                model: w.model,
                ...(w.prompt ? { prompt: w.prompt } : {}),
                ...(w.harness ? { harness: w.harness } : {}),
              },
            }
          : {}),
      })),
    ),
  };
}
function fault(status, detail) {
  throw Object.assign(new Error(detail), { status });
}
function page(items, url, max = 4) {
  const start = Number(url.searchParams.get("cursor") || 0),
    limit = Math.min(Number(url.searchParams.get("limit") || max), max),
    end = start + limit;
  return {
    items: items.slice(start, end),
    nextCursor: end < items.length ? String(end) : null,
  };
}
function task(id) {
  const t = data.tasks.find((t) => t.id === id);
  if (!t) fault(404, "This sample task is unavailable.");
  return {
    ...t,
    revision: 1,
    author: user,
    taskAttachments: [],
    heat: null,
    publishedAt: null,
  };
}
function works(id) {
  return (data.worksByTask[id] || []).map((w) => ({
    ...w,
    taskId: id,
    description: "",
    artifacts: [{ kind: "text", text: w.text }],
    author: user,
    approvedAt: null,
  }));
}
export async function previewApi(path, { method = "GET", body } = {}) {
  const url = new URL(path, "https://local.preview"),
    s = state(),
    route = url.pathname;
  await new Promise((r) => setTimeout(r, 100));
  if (route === "/auth/google") {
    s.signed = true;
    persist();
    return clone(session);
  }
  if (route === "/auth/logout") {
    s.signed = false;
    persist();
    return null;
  }
  if (route === "/me") {
    if (!s.signed) fault(401, "Sign in to the local preview.");
    return clone(session);
  }
  if (method !== "GET" && !s.signed)
    fault(401, "Sign in to the local preview.");
  if (route === "/tasks" && method === "GET") {
    const q = (url.searchParams.get("q") || "").toLowerCase(),
      category = url.searchParams.get("category");
    const items = data.tasks
      .filter(
        (t) =>
          (!category || t.category === category) &&
          (!q ||
            [t.title, t.summary, t.description]
              .join(" ")
              .toLowerCase()
              .includes(q)),
      )
      .map((t) => task(t.id));
    return page(items, url);
  }
  if (route === "/tasks" && method === "POST") {
    const id = "preview-draft-" + Object.keys(s.drafts).length;
    const record = {
      kind: "task",
      id,
      revision: 1,
      state: "draft",
      content: clone(body),
      reviewId: null,
      rejectionReason: null,
    };
    s.drafts[id] = record;
    persist();
    return clone(record);
  }
  const own = route.match(/^\/me\/tasks\/([^/]+)(\/review-requests)?$/);
  if (own) {
    const r = s.drafts[own[1]];
    if (!r) fault(404, "Local draft not found.");
    if (method === "PUT") {
      r.content = clone(body.content);
      r.revision++;
    }
    if (own[2]) {
      r.state = "pending_review";
      r.reviewId = "preview-review";
    }
    persist();
    return clone(r);
  }
  const match = route.match(/^\/tasks\/([^/]+)(?:\/(.*))?$/);
  if (match) {
    const id = decodeURIComponent(match[1]),
      action = match[2];
    task(id);
    if (!action) return task(id);
    if (action === "entries" && method === "GET")
      return page(works(id), url, 100);
    if (action === "entries" && method === "POST") {
      const record = {
        kind: "entry",
        id: "preview-entry-" + Date.now(),
        taskId: id,
        revision: 1,
        state: "pending_review",
        content: clone(body),
      };
      s.entries[record.id] = record;
      persist();
      return record;
    }
    const eligibility = {
      taskId: id,
      canVote: !s.closed[id],
      permanentlyClosed: !!s.closed[id],
      closedAt: s.closed[id] || null,
      reason: s.closed[id] ? "statistics_viewed" : null,
    };
    if (action === "voting-eligibility") return eligibility;
    if (action === "ballot")
      return {
        id: "preview-ballot-" + id,
        taskId: id,
        catalogRevision: 1,
        generation: 1,
        expiresAt: "2099-01-01T00:00:00Z",
        entries: works(id),
        currentVote: s.votes[id] || null,
      };
    if (action === "vote") {
      if (s.closed[id]) fault(403, "Voting is closed in this local preview.");
      s.votes[id] = {
        id: "preview-vote-" + id,
        entryId: body.entryId,
        revision: (s.votes[id]?.revision || 0) + 1,
      };
      persist();
      return clone(s.votes[id]);
    }
    if (action === "statistics-access") {
      if (!body.permanentlyCloseVoting)
        fault(422, "Confirm the permanent voting change.");
      s.closed[id] ||= new Date().toISOString();
      persist();
      const entries = works(id);
      return {
        taskId: id,
        eligibility: {
          ...eligibility,
          canVote: false,
          permanentlyClosed: true,
        },
        totalVotes: id === "concept-library" ? 20 : 0,
        humanVotes: id === "concept-library" ? 10 : 0,
        agentVotes: id === "concept-library" ? 10 : 0,
        entryVotes: entries.map((w, i) => ({
          entryId: w.id,
          side: w.side,
          votes: id === "concept-library" ? [6, 4, 5, 5][i] : 0,
          voteShare:
            id === "concept-library" ? [0.3, 0.2, 0.25, 0.25][i] : null,
        })),
      };
    }
    if (action === "comments") {
      if (method === "POST") {
        const comment = {
          id: "preview-comment-" + Date.now(),
          taskId: id,
          body: body.body,
          author: user,
          createdAt: new Date().toISOString(),
        };
        s.comments.push(comment);
        persist();
        return comment;
      }
      return page(
        s.comments.filter((c) => c.taskId === id),
        url,
        3,
      );
    }
  }
  if (route === "/reports") return { id: "preview-report", status: "open" };
  fault(
    501,
    "This action is not connected in the local design preview. No request was sent to the public API.",
  );
}
export function previewSession() {
  return state().signed ? clone(session) : null;
}

import TaskTitleField from "../components/TaskTitleField";
import SubmissionConflict from "../components/SubmissionConflict";
import { useState } from "react";
import useSubmissionStore from "../state/useSubmissionStore";
import { UploadBusy } from "../state/contexts";
import { emptyWork, isCompleteWork } from "../state/models";
import WorkEditor from "../components/WorkEditor";
import TaskAttachments from "../components/TaskAttachments";
import AdditionalContext from "../components/AdditionalContext";
import ErrorNote from "../components/ErrorNote";
import WorkDialog from "../components/WorkDialog";
import SubmissionPreview from "../components/SubmissionPreview";
import SubmissionHero from "../components/SubmissionHero";
import SectionHeading from "../components/SectionHeading";
import WorkChapter from "../components/WorkChapter";
import "../submit-design.css";
export default function Submit() {
  const store = useSubmissionStore();
  const {
    task,
    edit,
    entry,
    draft,
    record,
    loading,
    error,
    busy,
    locked,
    change,
    save,
    uploads,
    setUploads,
    preview,
    setPreview,
    message,
  } = store;
  const [editing, setEditing] = useState(task || entry ? 0 : null);
  const additional = !!(task || entry);
  const sides = additional ? [draft.entries[0]?.side || 'human'] : ['human', 'agent'];

  const stateLabel = locked
    ? record.state === "published" || record.state === "approved" ? "Published" : record.state === "taken_down" || record.state === "approval_revoked" ? "Unavailable" : "In review"
    : store.dirty ? "Unsaved changes"
      : record?.state === "rejected" ? "Returned"
        : record ? "Saved" : "New draft";
  if (loading) return <p role="status">Loading submission…</p>;
  if ((edit || entry) && !record)
    return <ErrorNote error={error} retry={() => location.reload()} />;
  const add = (side) => {
    change({ ...draft, entries: [...draft.entries, emptyWork(side)] });
    setEditing(draft.entries.length);
  };
  const update = (i, work) =>
    change({
      ...draft,
      entries: draft.entries.map((w, j) => (i === j ? work : w)),
    });
  const complete = (side) => {
    const works = draft.entries.filter((w) => w.side === side);
    return works.length > 0 && works.every(isCompleteWork);
  };
  return (
    <div className="submit-page">
      <div className="prototype-bar">
        <span>{"Your draft · Nothing is published before review."}</span>
        <a href="/me/submissions">My submissions</a>
        <a href="#review">Review & submit ↓</a>
      </div>
      <SubmissionHero additional={!!(task || entry)} />
      <UploadBusy.Provider value={(n) => setUploads((x) => x + n)}>
        <div className="editor-layout">
          <aside className="margin-index">
            <p className="eyebrow">On this page</p>
            <nav>
              <a href="#task">
                01 <span>The task</span>
              </a>
              <a href="#works">
                02 <span>The works</span>
              </a>
              <a href="#review">
                03 <span>Review</span>
              </a>
            </nav>
            <p className="draft-state">{stateLabel}</p>
            <p className="small">
              {record ? "Saved revision " + record.revision : "Not saved"}
            </p>
          </aside>
          <div className="editor-content">
            <form
              onSubmit={(e) => {
                e.preventDefault();
                save(true);
              }}
            >
              <fieldset disabled={busy || locked}>
                <section id="task" className="editor-section">
                  <SectionHeading
                    number="01"
                    label="The shared brief"
                    title={
                      <>
                        What are we
                        <br />
                        looking at?
                      </>
                    }
                    note="Give both perspectives the same starting point."
                  />
                  {task ? (
                    <AdditionalContext taskId={task} />
                  ) : entry ? (
                    <p>New works require review before appearing publicly.</p>
                  ) : (
                    <>
                      <TaskTitleField
                        value={draft.title}
                        onChange={(title) => change({ ...draft, title })}
                      />
                      <div className="paired-fields">
                        <label className="field">
                          Short introduction
                          <textarea
                            rows="4"
                            placeholder="Why does this task matter?"
                            value={draft.summary}
                            onChange={(e) =>
                              change({ ...draft, summary: e.target.value })
                            }
                          />
                        </label>
                        <label className="field">
                          Instructions & requirements
                          <textarea
                            rows="4"
                            placeholder="Describe the situation, shared facts and expected outcome."
                            value={draft.description}
                            onChange={(e) =>
                              change({ ...draft, description: e.target.value })
                            }
                          />
                        </label>
                      </div>
                      <label className="field category-field">
                        Category <span className="small">Optional</span>
                        <input
                          placeholder="e.g. Communication"
                          value={draft.category || ""}
                          onChange={(e) =>
                            change({ ...draft, category: e.target.value })
                          }
                        />
                      </label>
                      <div className="shared-input">
                        <h3>Given to everyone.</h3>
                        <p>The same source material, for every work. Optional task attachments.</p>
                        <TaskAttachments
                          value={draft.taskAttachments || []}
                          onChange={(taskAttachments) =>
                            change({ ...draft, taskAttachments })
                          }
                        />
                      </div>
                    </>
                  )}
                </section>
                <section id="works" className="editor-section">
                  <SectionHeading
                    number="02"
                    label="Your works"
                    title={
                      <>
                        {additional ? 'Another perspective.' : 'Two perspectives.'}
                        <br />
                        One shared task.
                      </>
                    }
                    note={additional ? 'Submit one complete work. The original task remains unchanged.' : 'Add at least one Human and one Agent work. Every added work must be complete before review.'}
                  />
                  <div className={additional ? "stacks single" : "stacks"}>
                    {sides.map((side) => (
                      <WorkChapter
                        key={side}
                        side={side}
                        works={draft.entries
                          .map((w, i) => ({ ...w, index: i }))
                          .filter((w) => w.side === side)}
                        add={() => add(side)}
                        edit={setEditing}
                        canAdd={(!task && !entry) || !draft.entries.length}
                      />
                    ))}
                  </div>
                  {(task || entry) && draft.entries.length === 1 && (
                    <button
                      type="button"
                      className="text-link"
                      onClick={() => setEditing(0)}
                    >
                      Edit work
                    </button>
                  )}
                </section>
                <section id="review" className="editor-section">
                  <SectionHeading
                    number="03"
                    label="Review & submit"
                    title={
                      <>
                        Read it as
                        <br />a reader would.
                      </>
                    }
                    note="Save unfinished thoughts. Send the complete task for review."
                  />
                  <div className="review-layout">
                    <div className="review-sheet">
                      <p className="eyebrow">Your task / cover sheet</p>
                      <h3>{additional ? draft.entries[0]?.title || 'Your work starts here.' : draft.title || "Your task starts here."}</h3>
                      <p>
                        {draft.summary ||
                          "Frame the situation, then bring both perspectives."}
                      </p>
                      <p className="small">
                        {draft.entries.filter((w) => w.side === "human").length}{" "}
                        Human added ({draft.entries.filter(w => w.side === 'human' && isCompleteWork(w)).length} complete) ·{" "}
                        {draft.entries.filter((w) => w.side === "agent").length}{" "}
                        Agent added ({draft.entries.filter(w => w.side === 'agent' && isCompleteWork(w)).length} complete) · {draft.taskAttachments?.length || 0} shared
                        files
                      </p>
                      <button
                        type="button"
                        className="text-link"
                        onClick={() => setPreview(!preview)}
                      >
                        Preview the submission ↗
                      </button>
                    </div>
                    <div>
                      <ul className="checklist">
                        {!additional && <li
                          className={
                            draft.title && draft.summary && draft.description
                              ? "complete"
                              : ""
                          }
                        >
                          Task title, introduction and instructions
                        </li>}
                        {sides.map((side) => (
                          <li
                            className={complete(side) ? "complete" : ""}
                            key={side}
                          >
                            {side === "human" ? "Human" : "Agent"} works: at least one added, all complete
                          </li>
                        ))}
                        <li
                          className={
                            sides.every(complete)
                              ? "complete"
                              : ""
                          }
                        >
                          Work content, sources, models and permissions
                        </li>
                        <li
                          className={
                            !draft.taskAttachments?.length
                              ? "optional"
                              : draft.taskAttachments.every(
                                    (a) =>
                                      typeof a.cloudUseAllowed === "boolean",
                                  )
                                ? "complete"
                                : ""
                          }
                        >
                          {draft.taskAttachments?.length
                            ? "Shared files and cloud-use permissions"
                            : "Shared files — optional, none added"}
                        </li>
                      </ul>
                      <p className="review-note">
                        {additional ? 'This work is reviewed independently. Approval adds it to the existing task; the original task is not reviewed again.' : 'Your task and initial works are reviewed together. Approval makes them public. Cloud-use permission is a separate choice for each work and attachment.'}
                      </p>
                      <div className="submit-actions">
                        {!task && !entry && (
                          <button
                            type="button"
                            className="button secondary"
                            disabled={uploads > 0}
                            onClick={() => save(false)}
                          >
                            Save draft
                          </button>
                        )}
                        <button className="button" disabled={uploads > 0}>
                          Submit for review
                        </button>
                      </div>
                      <p role="status">{message}</p>
                      <ErrorNote error={error} />
                      <SubmissionConflict store={store} />
                      {store.pending.current && (
                        <p className="small">
                          The previous request is unconfirmed. Retry with the same content and request identifier before continuing to edit.
                        </p>
                      )}
                      {record?.rejectionReason && (
                        <p>Reviewer note: {record.rejectionReason}</p>
                      )}
                    </div>
                  </div>
                </section>
              </fieldset>
            </form>
            {preview && (
              <SubmissionPreview draft={draft} close={() => setPreview(false)} />
            )}
          </div>
        </div>
        {editing !== null && draft.entries[editing] && (
          <WorkDialog close={() => setEditing(null)} side={draft.entries[editing].side} existing={!!draft.entries[editing].title}>
            <WorkEditor
              allowSideChange={!!(task || entry)}
              disabled={busy || locked}
              work={draft.entries[editing]}
              removable={!task && !entry}
              onChange={(w) => update(editing, w)}
              onRemove={() => {
                change({
                  ...draft,
                  entries: draft.entries.filter((_, i) => i !== editing),
                });
                setEditing(null);
              }}
            />
          </WorkDialog>
        )}
      </UploadBusy.Provider>
    </div>
  );
}

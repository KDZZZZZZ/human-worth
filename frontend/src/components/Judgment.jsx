import WorkReader from "./WorkReader";
import ChapterCover from "./ChapterCover";
import useVotingStore from "../state/useVotingStore";

import { api, apiUrl, enc, id } from "../api.js";
import ErrorNote from "./ErrorNote.jsx";
import Login from "./Login.jsx";
import Pager from "./Pager.jsx";
import Artifacts from "./Artifacts.jsx";
import Report from "./Report.jsx";
export default function Judgment({ taskId }) {
  const {
    auth,
    ballot,
    eligibility,
    selected,
    setSelected,
    error,
    busy,
    stats,
    confirm,
    setConfirm,
    message,
    side,
    setSide,
    positions,
    setPositions,
    base,
    refresh,
    works,
    vote,
    statistics,
    collection,
    current,
  } = useVotingStore(taskId);
  return (
    <section id="works" className="comparison">
      <div className="perspectives-heading">
        <p className="label">02 / PERSPECTIVES</p>
        <h2>
          One task.
          <br />
          Two perspectives.
        </h2>
        <div className="perspectives-context">
          <p className="small">
            Open a chapter, read each work, then return to your own judgment.
            Neither chapter implies a preference.
          </p>
          <div className="contribution-inline">
            <p>
              Another approach?
              <span>New works require review before appearing publicly.</span>
            </p>
            <a
              className="text-link"
              href={"/submit?task=" + encodeURIComponent(taskId)}
            >
              Add a work ↗
            </a>
          </div>
        </div>
      </div>
      {!auth.data ? (
        <>
          <Pager
            path={base + "/entries"}
            render={(w) => (
              <article className="work-sheet" key={w.id}>
                <p className="label">{w.side}</p>
                <h3>{w.title}</h3>
                <Artifacts
                  artifacts={w.artifacts}
                  base={apiUrl(base + "/entries/" + enc(w.id))}
                />
                <p className="small">{w.source}</p>
              </article>
            )}
          />
          <Login />
        </>
      ) : (
        <>
          <ErrorNote error={error} />
          {busy && <p role="status">Working…</p>}
          {!ballot && (
            <button disabled={busy} className="text-link" onClick={refresh}>
              Reload all works
            </button>
          )}
          {eligibility?.permanentlyClosed && (
            <p className="state-note">
              Voting is permanently closed for this task. Reading, discussion and adding works remain available.
            </p>
          )}
          {works && (
            <>
              <div className="chapter-entrances" hidden={!!side}>
                {["human", "agent"].map((s) => (
                  <ChapterCover
                    key={s}
                    side={s}
                    count={works.filter((w) => w.side === s).length}
                    open={() => setSide(s)}
                  />
                ))}
              </div>
              {current && (
                <div
                  className="reader"
                  tabIndex={0}
                  aria-label="Work reader"
                  onKeyDown={(e) => {
                    if (e.target.closest("input,textarea,select")) return;
                    if (e.key === "Escape") setSide(null);
                    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                      e.preventDefault();
                      setPositions({
                        ...positions,
                        [side]: Math.max(
                          0,
                          Math.min(
                            collection.length - 1,
                            (positions[side] || 0) +
                              (e.key === "ArrowRight" ? 1 : -1),
                          ),
                        ),
                      });
                    }
                  }}
                >
                  <div className="reader-toolbar">
                    <button className="text-link" onClick={() => setSide(null)}>
                      ← Back to Perspectives
                    </button>
                    <div className="reader-switch">
                      {["human", "agent"].map((s) => (
                        <button
                          key={s}
                          className="text-link"
                          aria-pressed={side === s}
                          onClick={() => setSide(s)}
                        >
                          {s === "human" ? "Human Work" : "Agent Work"}
                        </button>
                      ))}
                    </div>
                  </div>
                  <WorkReader
                    work={current}
                    side={side}
                    base={apiUrl(base + "/entries/" + enc(current.id))}
                    selected={selected === current.id}
                    choose={() =>
                      setSelected(selected === current.id ? "" : current.id)
                    }
                    page={(positions[side] || 0) + 1}
                    count={collection.length}
                  />
                  <div className="reader-pagination">
                    <button
                      disabled={!positions[side]}
                      onClick={() =>
                        setPositions({
                          ...positions,
                          [side]: positions[side] - 1,
                        })
                      }
                    >
                      ← Previous
                    </button>
                    <p>
                      {(positions[side] || 0) + 1} / {collection.length}
                    </p>
                    <button
                      disabled={(positions[side] || 0) >= collection.length - 1}
                      onClick={() =>
                        setPositions({
                          ...positions,
                          [side]: (positions[side] || 0) + 1,
                        })
                      }
                    >
                      Next →
                    </button>
                  </div>
                  <p className="reader-keyboard small">Use ← / → while reading to change works. Escape returns to Perspectives.</p>
                </div>
              )}
              <div className="judgment-guide">
                <p className="label">Your judgment</p>
                <p>
                  Having seen the different approaches, which work speaks to
                  you?
                </p>
              </div>
              <section className="current-choice" data-selected={!!selected}>
                <p className="label">Your current choice</p>
                <h3>
                  {works.find((w) => w.id === selected)?.title ||
                    "Choose a work as you read."}
                </h3>
                <p className="small">
                  Choosing a work only stages a preference. Confirm below to
                  submit it.

                </p>
                <div className="choice-actions">
                <button
                  className="button"
                  disabled={busy || !selected}
                  onClick={vote}
                >
                  Confirm my choice
                </button>
                <button className="text-link" onClick={() => setSelected("")}>
                  Cancel selection
                </button>
                </div>
                <p role="status">{message}</p>
              </section>
            </>
          )}
          {!works && eligibility && !eligibility.canVote && (
            <Pager
              path={base + "/entries"}
              render={(w) => (
                <article className="work-sheet" key={w.id}>
                  <h3>{w.title}</h3>
                  <Artifacts
                    artifacts={w.artifacts}
                    base={apiUrl(base + "/entries/" + enc(w.id))}
                  />
                </article>
              )}
            />
          )}
          <div className="statistics">
            <h3>Curious about other choices?</h3>
            <p>
              Viewing this task’s statistics permanently closes your future voting on
              this task. Reading works and adding a work remain available.
            </p>
            <button
              disabled={busy}
              className="text-link"
              onClick={() => setConfirm(true)}
            >
              View statistics…
            </button>
            {confirm && (
              <div className="state-note" role="alert">
                <p>
                  View statistics and close voting?
                </p>
                <p>This permanently removes your ability to vote or change your preference on this task. Any existing preference remains. You may still read, discuss and submit another work.</p>
                <button className="button" onClick={statistics}>
                  Close voting & view statistics
                </button>
                <button className="text-link" onClick={() => setConfirm(false)}>
                  Keep voting open
                </button>
              </div>
            )}
            {stats && (
              <>
                <p>
                  Preferences {stats.totalVotes} · Human {stats.humanVotes} · Agent{" "}
                  {stats.agentVotes}
                </p>
                <table>
                  <thead>
                    <tr>
                      <th>Work</th>
                      <th>Preferences</th>
                      <th>Share</th>
                    </tr>
                  </thead>
                  <tbody>
                    {stats.entryVotes.map((x) => (
                      <tr key={x.entryId}>
                        <td>
                          {works?.find((w) => w.id === x.entryId)?.title ||
                            x.entryId}
                        </td>
                        <td>{x.votes}</td>
                        <td>
                          {x.voteShare === null
                            ? "—"
                            : (x.voteShare * 100).toFixed(1) + "%"}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            )}
          </div>
        </>
      )}
    </section>
  );
}

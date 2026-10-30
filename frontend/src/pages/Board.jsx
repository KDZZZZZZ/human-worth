import { useEffect, useRef, useState } from "react";
import useLoad from "../hooks/useLoad";
import Load from "../components/Load";
import BoardGap from "../components/board/BoardGap";
import BoardKinds from "../components/board/BoardKinds";
import BoardTasks from "../components/board/BoardTasks";
import BoardArtwork from "../components/board/BoardArtwork";
import useBoardMotion from "../hooks/useBoardMotion";
import "../board-design.css";

export default function Board() {
  const state = useLoad("/board");
  const summaryState = state.data && (!state.data.overall || !Array.isArray(state.data.categories))
    ? { error: new Error("Board data is unavailable. Please try again."), reload: state.reload } : state;
  const [category, setCategory] = useState(new URLSearchParams(location.search).get("category") || "");
  const ref = useRef();
  useBoardMotion(ref);
  useEffect(() => { document.title = "Board — Human Worth"; }, []);
  function selectCategory(value, focus = true) {
    setCategory(value);
    const url = new URL(location.href);
    value ? url.searchParams.set("category", value) : url.searchParams.delete("category");
    url.searchParams.delete("shown");
    history.replaceState(history.state, "", url);
    if (focus) requestAnimationFrame(() => {
      ref.current.querySelector(".s3").scrollIntoView({ block: "start", behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
      ref.current.querySelector(".filter").focus({ preventScroll: true });
    });
  }
  return <div className="board-page">
    <div className="board" ref={ref}>
      <BoardArtwork />
      <div className="hero"><h1>Where do<br />perspectives diverge?</h1><p>Explore how conclusions differ across published tasks, and discover what people are discussing.</p></div>
      <section className="section s1" aria-labelledby="board-gap"><div className="intro"><h2 className="label" id="board-gap">The Gap</h2><div className="shortline" /><p className="description">How conclusions are distributed across all published tasks.</p></div>
        <div className="gap-body" aria-live="polite"><Load state={summaryState}>{(board) => <BoardGap overall={board.overall} />}</Load></div>
      </section>
      <section className="section s2" aria-labelledby="board-kind"><div className="intro"><h2 className="label" id="board-kind">By Kind</h2><div className="shortline" /><p className="description">Compare conclusion distributions by task category.</p></div>
        <div className="tablewrap" aria-live="polite"><Load state={summaryState}>{(board) => <BoardKinds categories={board.categories} selectCategory={selectCategory} />}</Load></div>
      </section>
      <section className="section s3" aria-labelledby="board-discussed"><div className="intro"><h2 className="label" id="board-discussed">Most Discussed</h2><div className="shortline" /><p className="description">Explore the most active task discussions from the last 7 days.</p><p className="muted counts">Based on unique commenting accounts and approved submissions. Discussion order is not a quality ranking.</p></div>
        <div className="tasks"><BoardTasks key={category} category={category} categories={(Array.isArray(state.data?.categories) ? state.data.categories : []).map((item) => item.category)} selectCategory={selectCategory} /></div>
      </section>
    </div>
  </div>;
}

import { useState, useEffect, useContext, useRef } from "react";
import { Auth } from "./contexts";

import { api, enc } from "../api";
export default function useVotingStore(taskId, works) {
  const auth = useContext(Auth),
    [ballot, setBallot] = useState(null),
    [eligibility, setEligibility] = useState(null),
    [selected, setSelected] = useState(""),
    [error, setError] = useState(null),
    [busy, setBusy] = useState(false),
    [stats, setStats] = useState(null),
    [confirm, setConfirm] = useState(false),
    [message, setMessage] = useState(""),
    [side, setSide] = useState(null),
    [positions, setPositions] = useState({ human: 0, agent: 0 });
  const epoch = useRef(0);
  const base = "/tasks/" + enc(taskId);
  async function refresh() {
    const version = ++epoch.current;
    setBallot(null);
    setSelected("");
    setError(null);
    setBusy(true);
    try {
      const e = await api(base + "/voting-eligibility");
      if (version !== epoch.current) return;
      setEligibility(e);
      if (e.canVote) {
        const b = await api(base + "/ballot");
        if (version !== epoch.current) return;
        setBallot(b);
        setSelected(b.currentVote?.entryId || "");
      } else setBallot(null);
    } catch (e) {
      if (version === epoch.current) setError(e);
    } finally {
      if (version === epoch.current) setBusy(false);
    }
  }
  useEffect(() => {
    setStats(null);
    setSelected("");
    setMessage("");
    setConfirm(false);
    if (auth.data) refresh();
    else {
      setBallot(null);
      setEligibility(null);
      setBusy(false);
    }
    return () => { epoch.current++; };
  }, [auth.data, taskId]);
  async function vote() {
    if (!eligibility?.canVote || !ballot || !ballot.entries.some((w) => w.id === selected)) return;
    setBusy(true);
    setError(null);
    try {
      const v = await api(base + "/vote", {
        method: "PUT",
        body: {
          ballotId: ballot.id,
          entryId: selected,
          expectedVoteRevision: ballot.currentVote?.revision || 0,
        },
      });
      setBallot({ ...ballot, currentVote: v });
      setMessage("Choice confirmed");
    } catch (e) {
      setError(e);
      if (e.status === 409) {
        setBallot(null);
        setSelected("");
      }
      if (e.status === 403) await refresh();
    } finally {
      setBusy(false);
    }
  }
  async function statistics() {
    epoch.current++;
    setConfirm(false);
    setBusy(true);
    setBallot(null);
    setSelected("");
    setError(null);
    try {
      setStats(
        await api(base + "/statistics-access", {
          method: "POST",
          body: { permanentlyCloseVoting: true },
        }),
      );
      setEligibility({ canVote: false, permanentlyClosed: true });
    } catch (e) {
      setError(e);
      try {
        setEligibility(await api(base + "/voting-eligibility"));
      } catch {
        setEligibility(null);
      }
    } finally {
      setBusy(false);
    }
  }
  const collection = (works || []).filter((w) => w.side === side),
    current =
      collection[
        Math.min(positions[side] || 0, Math.max(0, collection.length - 1))
      ];
  return {
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
  };
}

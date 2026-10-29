import ErrorNote from "./ErrorNote.jsx";
export default function Load({ state, children }) {
  return state.loading ? (
    <p role="status" className="state-note">
      Loading…
    </p>
  ) : state.error ? (
    <ErrorNote error={state.error} retry={state.reload} />
  ) : (
    children(state.data)
  );
}

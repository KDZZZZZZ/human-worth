import { useEffect, useRef } from "react";
export default function WorkDialog({ children, close, side, existing }) {
  const ref = useRef();
  useEffect(() => {
    ref.current.showModal();
  }, []);
  return (
    <dialog
      ref={ref}
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
    >
      <div className="dialog-heading">
        <p className="eyebrow">{side === "agent" ? "Agent work" : "Human work"}</p>
        <button
          className="close"
          type="button"
          aria-label="Close work editor"
          onClick={close}
        >
          ×
        </button>
      </div>
      <h2>{existing ? "A closer look." : "Add a work."}</h2>
      {children}
      <button className="button" type="button" onClick={close}>
        Keep in this draft
      </button>
    </dialog>
  );
}

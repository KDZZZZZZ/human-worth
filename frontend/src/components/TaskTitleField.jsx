import { useLayoutEffect, useRef } from "react";
export default function TaskTitleField({ value, onChange }) {
  const ref = useRef();
  useLayoutEffect(() => {
    ref.current.style.height = "0px";
    ref.current.style.height = ref.current.scrollHeight + "px";
  }, [value]);
  return (
    <label className="field title-field">
      Task title
      <textarea
        ref={ref}
        rows={1}
        aria-label="Task title"
        placeholder="What is the task?"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  );
}

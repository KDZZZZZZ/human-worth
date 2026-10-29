import { useState } from "react";

export default function FilePicker({ label, accept, disabled, onChange }) {
  const [filename, setFilename] = useState("");
  return (
    <span className="file-picker">
      <span className="file-picker-button" aria-hidden="true">Choose File</span>
      <span className="file-picker-name" aria-hidden="true">{filename || "No file chosen"}</span>
      <input
        className="file-picker-input"
        type="file"
        aria-label={label}
        accept={accept}
        disabled={disabled}
        onChange={(event) => {
          setFilename(event.target.files[0]?.name || "");
          onChange(event);
        }}
      />
    </span>
  );
}

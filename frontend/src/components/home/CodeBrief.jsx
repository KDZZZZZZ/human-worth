export default function CodeBrief() {
  return <div className="code-sheet">
    <header><span className="material-label">Starting code</span><span className="filename">save-note.js</span></header>
    <pre aria-label="Starting code supplied with the concept task"><code><span className="code-keyword">async function</span>{' saveNote(note) {\n  showStatus('}<span className="code-string">"Saving…"</span>{');\n\n  '}<span className="code-keyword">try</span>{' {\n    '}<span className="code-keyword">await</span>{' fetch('}<span className="code-string">"/notes"</span>{', {\n      method: '}<span className="code-string">"POST"</span>{',\n      body: JSON.stringify(note)\n    });\n  } '}<span className="code-keyword">finally</span>{' {\n    clearDraft();\n    showStatus('}<span className="code-string">"Saved"</span>{');\n  }\n}'}</code></pre>
  </div>;
}

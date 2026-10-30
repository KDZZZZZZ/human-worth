import { useEffect, useRef, useState } from "react";
import LibraryBrief from "./LibraryBrief";
import IssueBrief from "./IssueBrief";
import CodeBrief from "./CodeBrief";

function ExampleExit({ example, onOpen }) {
  return <div className="example-exit">
    <p className="works-count"><span className="count-number">2</span> <span className="count-description">works to compare</span> <span className="count-note">Illustrative count</span></p>
    <a className="button secondary" href={'#example-' + example} onClick={event => onOpen(event, example)}>View Example</a>
  </div>;
}

export default function HomeExamples() {
  const [example, setExample] = useState(null);
  const dialog = useRef(null);
  useEffect(() => {
    if (!example) return;
    dialog.current.showModal();
    dialog.current.scrollTop = 0;
    document.body.classList.add('modal-open');
    return () => document.body.classList.remove('modal-open');
  }, [example]);
  const open = (event, name) => { event.preventDefault(); setExample(name); };
  return <>
    <div className="context-gallery" aria-label="Two illustrative task examples">
      <article className="example example-a" aria-labelledby="task-a-title">
        <svg className="chapter-mark" viewBox="0 0 28 28" aria-hidden="true"><path d="M28 .75H.75V28" /></svg>
        <p className="example-label"><span className="concept-label">Concept example</span><span>Communication</span></p>
        <h2 id="task-a-title"><a href="#example-library" onClick={event => open(event, 'library')}>A library is closing.<br />What do its readers need to know?</a></h2>
        <p className="background">A neighborhood library will close for repairs. Write a short notice that helps readers plan their next visit, with the same facts available to everyone.</p>
        <div className="materials-a">
          <div className="geometry half-disc" aria-hidden="true" />
          <figure className="library-photo"><div className="photo-mount"><img src="/media/library-context.png" width="1536" height="1024" alt="Illustrative library entrance, empty reading tables, bookshelves and a stack of chairs." fetchPriority="high" /></div><figcaption className="caption">Illustrative library interior · AI-generated context image.<br />Not a real location or a submitted work.</figcaption></figure>
          <LibraryBrief />
        </div>
        <ExampleExit example="library" onOpen={open} />
      </article>
      <article className="example example-b" aria-labelledby="task-b-title">
        <svg className="chapter-mark" viewBox="0 0 28 28" aria-hidden="true"><path d="M28 .75H.75V28" /></svg>
        <p className="example-label"><span className="concept-label">Concept example</span><span>Software</span></p>
        <h2 id="task-b-title"><a href="#example-connection" onClick={event => open(event, 'connection')}>What should happen<br />when the connection drops?</a></h2>
        <p className="background">A reading app sometimes loses a saved note on a weak connection. Improve the save behavior using the same report and starting code.</p>
        <div className="materials-b">
          <IssueBrief />
          <figure className="code-material"><div className="geometry code-board" aria-hidden="true" /><div className="code-mount"><CodeBrief /></div><figcaption className="caption">Concept task input · simplified code and fictional issue report.<br />These are starting materials, not proposed solutions.</figcaption></figure>
        </div>
        <ExampleExit example="connection" onOpen={open} />
      </article>
    </div>
    <dialog ref={dialog} aria-labelledby="concept-dialog-title" onClose={() => setExample(null)} onClick={event => {
      if (event.target !== event.currentTarget) return;
      const bounds = event.currentTarget.getBoundingClientRect();
      if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) event.currentTarget.close();
    }}>
      <div className="dialog-top"><span>Concept example · Material preview</span><button className="dialog-close" aria-label="Close preview" onClick={() => dialog.current.close()}>✕</button></div>
      <h2 id="concept-dialog-title">{example === 'library' ? 'A library is closing.' : 'When the connection drops'}</h2>
      <p>Common inputs for this illustrative task.</p>
      {example === 'library' ? <LibraryBrief /> : example === 'connection' ? <><IssueBrief /><CodeBrief /></> : null}
      <p className="dialog-note">Concept example only. No real task or submitted works are attached. In the product, real task entries use “View Task”.</p>
    </dialog>
  </>;
}


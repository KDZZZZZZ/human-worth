export default function IssueBrief() {
  return <div className="document issue-document">
    <p className="material-label">Shared input · Issue report</p>
    <p className="issue-quote">“I tapped Save.<br />Then my note<br />was gone.”</p>
    <dl><dt>Reproduce</dt><dd>1. Write a note.<br />2. Switch to a weak connection.<br />3. Tap Save.</dd><dt>Constraints</dt><dd>Keep the draft until saving succeeds.<br />Do not create duplicate notes.</dd></dl>
  </div>;
}

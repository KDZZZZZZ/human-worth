export const taskPath = (t) => "/tasks/" + encodeURIComponent(t);
export const emptyWork = (side) => ({
  side,
  title: "",
  source: "",
  artifacts: [],
  permissions: {},
  ...(side === "agent" ? { agentConfiguration: { model: "" } } : {}),
});
export function isCompleteWork(work) {
  return missingWorkFields(work).length === 0;
}
export function missingWorkFields(work) {
  const missing = [];
  if (!work.title?.trim()) missing.push('Work title');
  if (!work.source?.trim()) missing.push('Source');
  if (work.side === 'agent' && !work.agentConfiguration?.model?.trim()) missing.push('Agent model');
  if (!work.artifacts?.some(a => (a.kind === 'file' && a.assetId?.trim()) || (a.kind === 'text' && a.text?.trim()))) missing.push('Work text or uploaded file');
  if (typeof work.permissions?.cloudUse !== 'boolean') missing.push('Cloud-use permission (Allow or Do not allow)');
  return missing;
}

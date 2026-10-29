export const taskPath = (t) => "/tasks/" + encodeURIComponent(t);
export const emptyWork = (side) => ({
  side,
  title: "",
  source: "",
  artifacts: [],
  permissions: { cloudUse: false },
  ...(side === "agent" ? { agentConfiguration: { model: "" } } : {}),
});
export function isCompleteWork(work) {
  return Boolean(
    work.title.trim() &&
      work.source.trim() &&
      typeof work.permissions?.cloudUse === "boolean" &&
      (work.side !== "agent" || work.agentConfiguration?.model?.trim()) &&
      work.artifacts.some(
        (a) => a.kind === "file" || (a.kind === "text" && a.text.trim()),
      ),
  );
}

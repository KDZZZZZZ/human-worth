export function taskReturn() {
  const raw = new URLSearchParams(location.search).get("return");
  if (!raw || /^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(raw) || raw.includes("\\")) return "/discovery";
  try {
    const url = new URL(raw, location.origin + "/");
    return ["/board", "/board.html", "/discovery", "/discovery.html"].includes(url.pathname) ? url.pathname + url.search : "/discovery";
  } catch { return "/discovery"; }
}

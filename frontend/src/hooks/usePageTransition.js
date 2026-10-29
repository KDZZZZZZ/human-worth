import { useEffect } from "react";

// Keep ordinary document navigation (including history and beforeunload guards).
export default function usePageTransition() {
  useEffect(() => {
    let animation, pending = false, recovery;
    const reduced = matchMedia("(prefers-reduced-motion: reduce)");
    const reset = () => {
      animation?.cancel();
      pending = false;
      clearTimeout(recovery);
    };
    const navigate = async (event) => {
      if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const link = event.target.closest("a[href]");
      if (!link || link.hasAttribute("download") || (link.target && link.target !== "_self")) return;
      const url = new URL(link.href);
      if (url.origin !== location.origin || url.pathname.startsWith("/api/") || (url.pathname === location.pathname && url.search === location.search)) return;
      if (reduced.matches) return;
      const main = document.querySelector("#main");
      if (!main?.animate) return;
      event.preventDefault();
      if (pending) return;
      pending = true;
      animation = main.animate([
        { opacity: 1, transform: "translateY(0)" },
        { opacity: .25, transform: "translateY(-8px)" },
      ], { duration: 160, easing: "ease-in", fill: "forwards" });
      await animation.finished.catch(() => {});
      // A cancelled unload prompt or slow navigation must not leave the page faded.
      recovery = setTimeout(reset, 500);
      location.assign(url.href);
    };
    document.addEventListener("click", navigate);
    window.addEventListener("pageshow", reset);
    reduced.addEventListener("change", reset);
    return () => {
      reset();
      document.removeEventListener("click", navigate);
      window.removeEventListener("pageshow", reset);
      reduced.removeEventListener("change", reset);
    };
  }, []);
}

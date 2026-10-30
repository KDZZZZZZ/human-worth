import { useEffect } from "react";
import { threadPath } from "../components/board/BoardArtwork";

export default function useBoardMotion(ref) {
  useEffect(() => {
    const board = ref.current, svg = board.querySelector(".thread"), path = svg.querySelector("path");
    const reduced = matchMedia("(prefers-reduced-motion: reduce)");
    const sections = [...board.querySelectorAll(".section")];
    const observer = new IntersectionObserver((entries) => entries.forEach((entry) => {
      if (entry.isIntersecting) { entry.target.classList.add("visible"); observer.unobserve(entry.target); }
    }), { rootMargin: "0px 0px -12% 0px" });
    sections.forEach((section) => {
      section.classList.add("reveal-ready");
      if (reduced.matches) section.classList.add("visible");
      else observer.observe(section);
    });
    let frame, progress = 0;
    const positionKey = () => "human-worth:board-position:" + location.pathname + location.search;
    let restored = false, target = 0;
    try { target = Number(sessionStorage.getItem(positionKey())) || 0; } catch {}
    const savePosition = () => { if (restored) { try { sessionStorage.setItem(positionKey(), String(scrollY)); } catch {} } };
    function draw() {
      frame = null;
      const matrix = path.getScreenCTM();
      if (!matrix) return;
      const length = path.getTotalLength();
      let low = 0, high = length;
      for (let index = 0; index < 18; index++) {
        const mid = (low + high) / 2, point = path.getPointAtLength(mid);
        if (matrix.b * point.x + matrix.d * point.y + matrix.f < innerHeight * .84) low = mid;
        else high = mid;
      }
      progress = reduced.matches ? 1 : Math.max(progress, low / length);
      path.style.strokeDasharray = `${length} ${length}`;
      path.style.strokeDashoffset = String(length * (1 - progress));
      svg.querySelectorAll("circle").forEach((circle) => {
        const y = matrix.b * Number(circle.getAttribute("cx")) + matrix.d * Number(circle.getAttribute("cy")) + matrix.f;
        if (reduced.matches || y < innerHeight * .84 || progress > .999) circle.style.opacity = "1";
        else if (circle.style.opacity !== "1") circle.style.opacity = "0";
      });
    }
    function schedule() { if (!frame) frame = requestAnimationFrame(draw); }
    function resize() {
      const width = board.clientWidth, height = board.offsetHeight;
      if (width >= 760) {
        svg.setAttribute("viewBox", "0 0 763 1404");
        svg.style.height = `${width * 1404 / 763}px`;
        path.setAttribute("d", threadPath);
        svg.querySelectorAll("g circle").forEach((circle, index) => {
          const [x, y] = [[192, 463], [193, 767], [199, 1076]][index];
          circle.setAttribute("cx", x); circle.setAttribute("cy", y);
        });
        svg.lastElementChild.setAttribute("cx", "195"); svg.lastElementChild.setAttribute("cy", "1333");
      } else {
        svg.style.height = "100%";
        svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
        let previous = 270, curve = "M 23 270";
        sections.forEach((section, index) => {
          const y = section.offsetTop + 48;
          curve += ` C -7 ${previous + 40} 53 ${y - 55} 23 ${y}`;
          const circle = svg.querySelectorAll("g circle")[index];
          circle.setAttribute("cx", "23"); circle.setAttribute("cy", y);
          previous = y;
        });
        curve += ` C -7 ${previous + 80} 68 ${height - 65} 23 ${height - 25}`;
        path.setAttribute("d", curve);
        svg.lastElementChild.setAttribute("cx", "23"); svg.lastElementChild.setAttribute("cy", height - 25);
      }
      if (!restored && !board.querySelector(".tasks .list-end [role='status']")) {
        scrollTo({ top: target, behavior: "instant" });
        restored = true;
      }
      schedule();
    }
    const change = () => { if (reduced.matches) sections.forEach((section) => section.classList.add("visible")); schedule(); };
    const size = new ResizeObserver(resize);
    size.observe(board);
    window.addEventListener("resize", resize);
    window.addEventListener("scroll", schedule, { passive: true });
    window.addEventListener("scroll", savePosition, { passive: true });
    window.addEventListener("pagehide", savePosition);
    reduced.addEventListener("change", change);
    resize();
    return () => { cancelAnimationFrame(frame); size.disconnect(); observer.disconnect(); window.removeEventListener("resize", resize); window.removeEventListener("scroll", schedule); window.removeEventListener("scroll", savePosition); window.removeEventListener("pagehide", savePosition); reduced.removeEventListener("change", change); };
  }, [ref]);
}

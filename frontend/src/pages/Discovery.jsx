import DiscoveryEmpty from "../components/DiscoveryEmpty";
import { useState } from "react";
import { query } from "../api";
import Pager from "../components/Pager";
import TaskCard from "../components/TaskCard";
import useTaskCategories from "../hooks/useTaskCategories";
import ErrorNote from "../components/ErrorNote";

import "../discovery-design.css";
export default function Discovery() {
  const params = new URLSearchParams(location.search);
  const [search, setSearch] = useState(params.get("q") || "");
  const category = params.get("category") || "";
  const { categories, error, retry } = useTaskCategories();
    const url = (c) => "/discovery" + query({ q: search, category: c, order: params.get("order") === "hot" ? "hot" : undefined });
  return (
    <div className="discovery-page">
      <header className="page-intro">
        <svg className="open-margin" viewBox="0 0 146 138" aria-hidden="true">
          <rect
            className="paper-square"
            x="16"
            y="8"
            width="110"
            height="110"
          />
          <rect
            className="small-square"
            x="103"
            y="94"
            width="42"
            height="42"
          />
          <path className="desktop-angle" d="M40 .75H.75V40" />
          <path className="mobile-angle" d="M146 4H4V138" />
        </svg>
        <div className="intro-copy">
          <h1>{"Discovery"}</h1>
          <p className="description">Works, contexts, and perspectives.</p>
        </div>
        <p className="prototype-note">
          <strong>Published tasks</strong>Read, compare, and share your perspective.
        </p>
      </header>

      <div className="browse-content">
        <aside className="index-margin">
          <h2>Categories</h2>
          <div className="category-index">
            <a
              className="category-filter"
              aria-current={!category ? "true" : undefined}
              href={url("")}
            >
              All tasks
            </a>
            {categories.map((c) => (
              <a
                key={c}
                className="category-filter"
                aria-current={category === c ? "true" : undefined}
                href={url(c)}
              >
                {c}
              </a>
            ))}
          </div>
          <p className="index-note">Categories from published tasks.</p>
          <ErrorNote error={error} retry={retry} />
        </aside>
        <section className="task-content">
          <form
            id="task-search-form"
            className="search-form"
            onSubmit={(e) => {
              e.preventDefault();
              location.href = url(category);
            }}
          >
            <div className="search-field">
              <label className="sr-only" htmlFor="task-search">
                Search task titles and descriptions
              </label>
              <span className="search-icon" aria-hidden="true">
                ⌕
              </span>
              <input
                id="task-search"
                type="search"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search tasks"
                maxLength="200"
              />
            </div>
            <button className="button">Search</button>
          </form>
          <p className="search-status">Explore the tasks below.</p>
          <Pager
            moreLabel="Load more tasks"
            empty={<DiscoveryEmpty />}
            auto
            path={
              "/tasks" +
              query({
                order: params.get("order") === "hot" ? "hot" : "recommended",
                q: params.get("q"),
                category: params.get("category"),
              })
            }
            render={(task) => <TaskCard key={task.id} task={task} />}
          />
        </section>
      </div>
    </div>
  );
}

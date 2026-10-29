import React from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { SessionProvider } from "./state/SessionProvider";
import "./style.css";
import "./app.css";
createRoot(document.getElementById("root")).render(
  <SessionProvider>
    <App />
  </SessionProvider>,
);

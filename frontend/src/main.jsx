import { createRoot } from "react-dom/client";
import "katex/dist/katex.min.css";
import "./styles/tokens.css";
import "./styles/base.css";
import "./styles/components.css";
import "./shell/shell.css";
import "./styles/markdown.css";
import App from "./App.jsx";
import ErrorBoundary, { reportClientError } from "./components/ErrorBoundary.jsx";
import { applyTheme, storedTheme } from "./app/theme.js";
import { isDesktop, isMac, platform } from "./lib/native.js";

// Paint in the remembered theme before anything renders.
applyTheme(storedTheme());

// Host classes: the desktop app's window (traffic lights, drag regions) and the platform.
document.documentElement.classList.toggle("desktop", isDesktop);
document.documentElement.classList.toggle("mac", isMac);
document.documentElement.dataset.platform = platform;

window.addEventListener("error", (event) => reportClientError(event.error || event.message));
window.addEventListener("unhandledrejection", (event) => reportClientError(event.reason));

createRoot(document.getElementById("root")).render(
  <ErrorBoundary>
    <App />
  </ErrorBoundary>,
);

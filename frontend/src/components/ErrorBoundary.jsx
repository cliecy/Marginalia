import { Component } from "react";
import { API_BASE, CLIENT_HEADERS, apiUrl } from "../lib/api.js";

/** Sends a browser error to the server log (best effort, never throws). */
export function reportClientError(error, extra = "") {
  try {
    const body = JSON.stringify({
      message: String(error?.message || error).slice(0, 2000),
      stack: String(error?.stack || "").slice(0, 6000) + (extra ? `\n${extra}`.slice(0, 4000) : ""),
      url: location.href,
      userAgent: navigator.userAgent,
    });
    fetch(apiUrl("/api/client-error"), {
      method: "POST",
      headers: { ...CLIENT_HEADERS, "Content-Type": "application/json" },
      credentials: API_BASE ? "include" : "same-origin",
      body,
    }).catch(() => {});
  } catch {}
}

/** Shows the error instead of a blank page, and reports it to the server log. */
export default class ErrorBoundary extends Component {
  state = { error: null };

  static getDerivedStateFromError(error) {
    return { error };
  }

  componentDidCatch(error, info) {
    reportClientError(error, info?.componentStack || "");
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="gate">
        <div className="gate-card gate-wide">
          <span className="gate-mark gate-mark-red" aria-hidden="true">!</span>
          <h1>页面出错了</h1>
          <p className="gate-sub">错误已记录到日志。你的笔记不受影响，已保存的内容都在。</p>
          <pre className="gate-pre">{String(this.state.error?.message || this.state.error)}</pre>
          <div className="gate-actions">
            <button type="button" className="btn btn-primary btn-md" onClick={() => location.reload()}>
              <span className="btn-label">重新加载</span>
            </button>
          </div>
        </div>
      </div>
    );
  }
}

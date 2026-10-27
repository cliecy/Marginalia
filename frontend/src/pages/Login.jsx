import { useRef, useState } from "react";
import "./Login.css";
import { api } from "../lib/api.js";
import { Button, Icon, Logo } from "../ui/index.js";

/**
 * Password sign-in for the browser (`marginalia serve`): one field with a show/hide eye and a caps-lock hint; a wrong
 * password shakes the field and keeps it, selected, for another try.
 *
 * @param {object} props
 * @param {() => Promise<void>} props.onDone reloads /api/auth/me after a successful login
 */
export default function Login({ onDone }) {
  const [password, setPassword] = useState("");
  const [shown, setShown] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [caps, setCaps] = useState(false);
  const [shaking, setShaking] = useState(false);
  const input = useRef(null);

  const submit = async (event) => {
    event.preventDefault();
    if (!password || busy) return;
    setBusy(true);
    setError("");
    try {
      await api.login(password);
      await onDone();
    } catch (err) {
      setError(err.status === 401 ? "密码不对，再试一次" : err.message);
      setShaking(true);
      setBusy(false);
      requestAnimationFrame(() => input.current?.select());
    }
  };

  const readCaps = (event) => setCaps(!!event.getModifierState?.("CapsLock"));

  return (
    <div className="gate login">
      <form className="gate-card login-card" onSubmit={submit} noValidate>
        <Logo size={46} />
        <h1>Marginalia</h1>
        <p className="gate-sub">输入密码，打开你的知识库</p>

        <div className="gate-form">
          <div
            className={`login-field ${error ? "has-error" : ""} ${shaking ? "login-shake" : ""}`.replace(/\s+/g, " ").trim()}
            onAnimationEnd={() => setShaking(false)}
          >
            <Icon name="lock" size={14} className="login-lock" />
            <input
              ref={input}
              className="input input-lg login-input"
              type={shown ? "text" : "password"}
              name="password"
              autoComplete="current-password"
              autoFocus
              spellCheck={false}
              placeholder="密码"
              aria-label="密码"
              aria-invalid={!!error || undefined}
              aria-describedby={error ? "login-error" : undefined}
              value={password}
              onChange={(event) => {
                setPassword(event.target.value);
                setError("");
              }}
              onKeyDown={readCaps}
              onKeyUp={readCaps}
              onBlur={() => setCaps(false)}
            />
            <button
              type="button"
              className="login-eye"
              tabIndex={-1}
              aria-label={shown ? "隐藏密码" : "显示密码"}
              title={shown ? "隐藏密码" : "显示密码"}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => setShown(!shown)}
            >
              <Icon name={shown ? "eye-off" : "eye"} size={14} />
            </button>
          </div>

          {error ? (
            <div id="login-error" className="login-msg login-err" role="alert">
              <Icon name="alert-circle" size={13} />
              <span>{error}</span>
            </div>
          ) : caps ? (
            <div className="login-msg login-caps">
              <Icon name="arrow-up" size={13} />
              <span>大写锁定已打开</span>
            </div>
          ) : null}

          <Button type="submit" variant="primary" size="lg" loading={busy} disabled={!password}>
            登录
          </Button>
        </div>

        <p className="gate-foot">忘了密码？它在服务器数据目录的 password 文件里。</p>
      </form>
    </div>
  );
}

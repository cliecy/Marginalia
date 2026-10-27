import { useEffect, useRef, useState } from "react";
import { originProblem } from "../../lib/appApi.js";
import { Button, Dialog, Icon, Input, Skeleton, Spinner, TextArea, Toggle, toast } from "../../ui/index.js";
import { isComposingKey } from "../../ui/keys.js";

/**
 * A switch that saves itself: it flips at once, waits for the save, and
 * flips back (with a toast) when the save fails.
 *
 * @param {object} props
 * @param {boolean} props.checked  the saved value
 * @param {(next: boolean) => Promise<unknown>} props.onSave
 * @param {string} props.label     accessible name
 * @param {boolean} [props.disabled]
 */
export function SettingToggle({ checked, onSave, label, disabled = false }) {
  const [pending, setPending] = useState(null);
  const change = async (next) => {
    if (pending !== null) return;
    setPending(next);
    try {
      await onSave(next);
    } catch (err) {
      toast.err(`没能保存：${err.message}`);
    } finally {
      setPending(null);
    }
  };
  return <Toggle checked={pending ?? !!checked} onChange={change} label={label} disabled={disabled} />;
}

/**
 * A small field that saves when you press Enter or leave it — the way a Mac
 * settings window does — with Escape to undo. A spinner shows while it saves
 * and a tick for a moment after; a bad value gets a red edge and a toast.
 *
 * @param {object} props
 * @param {string|number} props.value  the saved value
 * @param {(next: string) => Promise<unknown>} props.onCommit
 * @param {(next: string) => string|null} [props.validate] an error message blocks the save
 * @param {number|string} [props.width]
 * @param {React.ReactNode} [props.suffix] a unit after the field ("秒")
 */
export function CommitInput({ value, onCommit, validate, width = 160, suffix, disabled = false, ...inputProps }) {
  const saved = value ?? "";
  const [draft, setDraft] = useState(String(saved));
  const [status, setStatus] = useState("idle");
  const [invalid, setInvalid] = useState(false);
  const focused = useRef(false);
  const escaping = useRef(false);
  const timer = useRef(null);

  useEffect(() => {
    if (!focused.current) setDraft(String(saved));
  }, [saved]);
  useEffect(() => () => clearTimeout(timer.current), []);

  const commit = async () => {
    const next = draft.trim();
    if (next === String(saved)) {
      setInvalid(false);
      setDraft(String(saved));
      return;
    }
    const problem = validate?.(next);
    if (problem) {
      setInvalid(true);
      toast.err(problem);
      return;
    }
    setStatus("saving");
    try {
      await onCommit(next);
      setInvalid(false);
      setStatus("saved");
      clearTimeout(timer.current);
      timer.current = setTimeout(() => setStatus("idle"), 1400);
    } catch (err) {
      setInvalid(true);
      setStatus("idle");
      toast.err(`没能保存：${err.message}`);
    }
  };

  return (
    <span className={`set-input ${disabled ? "is-disabled" : ""}`.trim()} style={{ width }}>
      <Input
        size="sm"
        value={draft}
        disabled={disabled}
        aria-invalid={invalid || undefined}
        onFocus={() => (focused.current = true)}
        onBlur={() => {
          focused.current = false;
          if (escaping.current) {
            escaping.current = false;
            return;
          }
          commit();
        }}
        onChange={(event) => {
          setDraft(event.target.value);
          setInvalid(false);
        }}
        onKeyDown={(event) => {
          if (isComposingKey(event)) return;
          if (event.key === "Enter") event.currentTarget.blur();
          if (event.key === "Escape") {
            event.stopPropagation();
            escaping.current = true;
            setDraft(String(saved));
            setInvalid(false);
            event.currentTarget.blur();
          }
        }}
        {...inputProps}
      />
      {suffix && <span className="set-suffix">{suffix}</span>}
      <span className="set-state" aria-live="polite">
        {status === "saving" ? <Spinner size={12} label="正在保存" /> : status === "saved" ? <Icon name="check" size={13} stroke={1.9} title="已保存" /> : null}
      </span>
    </span>
  );
}

/** Validates a whole number in a range; returns an error message or null. */
export const intProblem = (min, max, what) => (text) => {
  if (!/^\d+$/.test(text)) return `${what}需要是整数`;
  const n = Number(text);
  if (n < min || n > max) return `${what}需要在 ${min} 到 ${max} 之间`;
  return null;
};

/**
 * Edits the list of CORS origins: one per line, checked before saving.
 *
 * @param {object} props
 * @param {boolean} props.open
 * @param {string[]} props.origins
 * @param {(origins: string[]) => Promise<unknown>} props.onSave
 * @param {() => void} props.onClose
 */
export function OriginsDialog({ open, origins, onSave, onClose }) {
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setText((origins || []).join("\n"));
      setError("");
    }
  }, [open, origins]);

  const save = async () => {
    const list = [...new Set(text.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean))];
    const problem = list.map(originProblem).find(Boolean);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    try {
      await onSave(list);
      onClose();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="允许的 CORS 来源"
      icon="globe"
      width={460}
      footerNote="改动后本地 API 会重启一下。"
      footer={
        <>
          <Button onClick={onClose}>取消</Button>
          <Button variant="primary" loading={busy} onClick={save}>
            保存
          </Button>
        </>
      }
    >
      <div className="set-dialog">
        <p className="dlg-message">
          只有列在这里的网页可以在浏览器里直接调用本地 API（还需要令牌）。每行一个，只写协议、主机和端口。
        </p>
        <TextArea
          mono
          rows={5}
          value={text}
          placeholder={"http://localhost:5173\nhttps://my-dashboard.example.com"}
          aria-invalid={!!error || undefined}
          onChange={(event) => {
            setText(event.target.value);
            setError("");
          }}
          onKeyDown={(event) => {
            if (!isComposingKey(event) && (event.metaKey || event.ctrlKey) && event.key === "Enter") save();
          }}
          data-autofocus
        />
        {error ? <div className="field-error">{error}</div> : <div className="field-hint">留空表示不允许任何网页跨域调用。⌘↩ 保存。</div>}
      </div>
    </Dialog>
  );
}

/** Placeholder rows while the settings load. */
export function SettingsSkeleton({ rows = 3 }) {
  return (
    <div className="list" aria-hidden="true">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="row set-sk">
          <div className="who">
            <Skeleton width={`${18 + ((i * 11) % 14)}%`} height={10} />
            <Skeleton width={`${34 + ((i * 19) % 26)}%`} height={7} style={{ marginTop: 7 }} />
          </div>
          <Skeleton width={i % 2 ? 32 : 120} height={i % 2 ? 19 : 22} radius={i % 2 ? 10 : 6} />
        </div>
      ))}
    </div>
  );
}

import { useEffect, useState } from "react";
import { useApp } from "../../app/AppContext.jsx";
import { reloadModels, useModels } from "../../app/hooks.js";
import { api } from "../../lib/api.js";
import { Button, Field, Icon, IconButton, Input, List, Pill, Segmented, Select, Spinner, StatusDot, toast } from "../../ui/index.js";
import { isComposingKey } from "../../ui/keys.js";
import { envOverride } from "./env.js";
import { modelOptions } from "./modelOptions.js";
import { EnvNote, SetBlock, SetHead, SetRow } from "./search.jsx";

const PROVIDERS = [
  { value: "magpie", label: "Magpie", icon: "magpie" },
  { value: "custom", label: "自定义" },
];

/** "http://127.0.0.1:3425/v1" → "127.0.0.1:3425/v1". */
const shortUrl = (url) => (url || "").replace(/^https?:\/\//, "");

/** The outcome of the last connection test, in one line. */
function TestResult({ test }) {
  if (!test) return null;
  if (test.state === "running") {
    return (
      <span className="set-test">
        <Spinner size={12} /> 正在连接…
      </span>
    );
  }
  return (
    <span className={`set-test ${test.state === "ok" ? "ok" : "err"}`} title={test.text}>
      <Icon name={test.state === "ok" ? "check-circle" : "alert-circle"} size={13} />
      <span>{test.text}</span>
    </span>
  );
}

/**
 * AI 模型 — where the models come from (Magpie's gateway or any
 * OpenAI-compatible service), the write-only API key, the default model and a
 * connection test against /api/ai/models.
 */
export default function Ai() {
  const ctx = useApp();
  const ai = ctx.settings?.ai || {};
  const current = ai.provider === "magpie" ? "magpie" : "custom";
  const gateway = ctx.integrations?.magpie?.gateway || null;
  const models = useModels();
  const keyFileVar = envOverride(ctx, "ai.api_key");

  const [view, setView] = useState(current);
  const [baseUrl, setBaseUrl] = useState(current === "custom" ? ai.base_url || "" : "");
  const [apiKey, setApiKey] = useState("");
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);
  const [switching, setSwitching] = useState(false);
  const [test, setTest] = useState(null);

  // Follow changes made elsewhere (the Magpie page, another window).
  useEffect(() => {
    setView(current);
    if (current === "custom") setBaseUrl(ai.base_url || "");
  }, [current, ai.base_url]);

  const runTest = async () => {
    setTest({ state: "running" });
    const result = await reloadModels();
    setTest(result.error ? { state: "err", text: result.error } : { state: "ok", text: `连接正常 · ${result.models.length} 个模型` });
    return !result.error;
  };

  const savedBase = current === "custom" ? ai.base_url || "" : "";
  const dirty = baseUrl.trim().replace(/\/+$/, "") !== savedBase || apiKey.trim() !== "" || current !== "custom";

  const saveCustom = async () => {
    const url = baseUrl.trim().replace(/\/+$/, "");
    if (!/^https?:\/\/[^/\s]+/.test(url)) {
      setFormError("Base URL 需要以 http:// 或 https:// 开头");
      return;
    }
    const patch = { ai: { base_url: url } };
    if (apiKey.trim()) patch.ai.api_key = apiKey.trim();
    else if (current === "magpie") patch.ai.api_key = ""; // Magpie's placeholder key means nothing elsewhere
    setSaving(true);
    setFormError("");
    try {
      await ctx.updateSettings(patch);
      setApiKey("");
      ctx.reloadMe();
      const ok = await runTest();
      if (ok) toast.ok("已保存，连接正常");
    } catch (err) {
      setFormError(err.message);
    } finally {
      setSaving(false);
    }
  };

  const clearKey = async () => {
    try {
      await ctx.updateSettings({ ai: { api_key: "" } });
      ctx.reloadMe();
      toast.ok("已清除保存的密钥");
    } catch (err) {
      toast.err(err.message);
    }
  };

  const switchToMagpie = async () => {
    setSwitching(true);
    try {
      await api.magpieUse();
      await ctx.reloadSettings();
      ctx.reloadMe();
      await runTest();
      toast.ok("已改用 Magpie 的模型");
    } catch (err) {
      toast.err(`没能切换到 Magpie：${err.message}`);
    } finally {
      setSwitching(false);
    }
  };

  const setDefault = async (model) => {
    try {
      await ctx.updateSettings({ ai: { default_model: model } });
      ctx.reloadMe();
    } catch (err) {
      toast.err(`没能保存默认模型：${err.message}`);
    }
  };

  const options = modelOptions(models.models, models.labels, ai.default_model);
  const gatewayLine = gateway?.running
    ? [`运行中 · ${shortUrl(gateway.url) || "127.0.0.1:3425"}`, gateway.version && `v${gateway.version}`, gateway.models != null && `${gateway.models} 个模型`].filter(Boolean).join(" · ")
    : "没有检测到 Magpie 网关：先打开 Magpie 应用";

  return (
    <>
      <List>
        <SetRow
          title="模型来源"
          sub={current === "magpie" ? `正在使用 Magpie · ${shortUrl(ai.base_url)}` : ai.base_url ? `正在使用 ${shortUrl(ai.base_url)}` : "还没有配置模型"}
          keywords="模型 来源 provider magpie 自定义 openai"
          env="ai.base_url"
          right={<Segmented size="sm" value={view} onChange={setView} options={PROVIDERS} ariaLabel="模型来源" />}
        />
        {view === "magpie" && (
          <SetRow
            icon={<StatusDot tone={gateway?.running ? "green" : "grey"} ring={!!gateway?.running} />}
            title="Magpie 网关"
            sub={gatewayLine}
            keywords="magpie 网关 gateway"
            right={
              current === "magpie" ? (
                <Pill tone="green" icon="check">
                  使用中
                </Pill>
              ) : gateway?.running ? (
                <Button size="sm" variant="soft" icon="magpie" loading={switching} onClick={switchToMagpie}>
                  使用 Magpie
                </Button>
              ) : (
                <Button size="sm" variant="ghost" iconRight="chevron-right" onClick={() => ctx.go("magpie")}>
                  了解 Magpie
                </Button>
              )
            }
          />
        )}
      </List>

      {view === "custom" && (
        <SetBlock keywords="base url api key 密钥 地址 openai 兼容 自定义" className="set-form-wrap">
          <div className="card set-form">
            <Field label="Base URL" inline hint="OpenAI 兼容服务的地址，通常以 /v1 结尾。本机的 Ollama、LM Studio 也可以。">
              {(id) => (
                <Input
                  id={id}
                  mono
                  value={baseUrl}
                  placeholder="https://api.openai.com/v1"
                  aria-invalid={!!formError || undefined}
                  onChange={(event) => {
                    setBaseUrl(event.target.value);
                    setFormError("");
                  }}
                  onKeyDown={(event) => !isComposingKey(event) && event.key === "Enter" && dirty && saveCustom()}
                />
              )}
            </Field>
            <Field
              label="API 密钥"
              inline
              hint={
                <>
                  {keyFileVar ? (
                    <>
                      密钥文件的位置 <EnvNote name={keyFileVar} />；保存时写进那个文件，不会再显示出来。
                    </>
                  ) : (
                    "只保存在本机数据目录的 ai_api_key 文件里，不会再显示出来。本机地址可以不填。"
                  )}
                  {ai.has_key && current === "custom" && (
                    <button type="button" className="set-link" onClick={clearKey}>
                      清除已保存的密钥
                    </button>
                  )}
                </>
              }
            >
              {(id) => (
                <Input
                  id={id}
                  type="password"
                  mono
                  autoComplete="off"
                  value={apiKey}
                  placeholder={ai.has_key && current === "custom" ? "已保存 · 留空则不修改" : "sk-…"}
                  onChange={(event) => setApiKey(event.target.value)}
                  onKeyDown={(event) => !isComposingKey(event) && event.key === "Enter" && dirty && saveCustom()}
                />
              )}
            </Field>
            <div className="set-form-foot">
              {formError ? (
                <span className="set-test err">
                  <Icon name="alert-circle" size={13} />
                  <span>{formError}</span>
                </span>
              ) : (
                <span className="set-form-note">保存后会自动测试一次连接。</span>
              )}
              <Button size="sm" variant="primary" loading={saving} disabled={!dirty || !baseUrl.trim()} onClick={saveCustom}>
                {current === "custom" ? "保存" : "改用这个服务"}
              </Button>
            </div>
          </div>
        </SetBlock>
      )}

      <SetHead title="模型" note={models.loading ? null : models.models.length ? `${models.models.length} 个可用` : null} />
      <List>
        <SetRow
          title="默认模型"
          sub="新对话和整理知识库默认用它；AI 面板里可以临时换"
          keywords="默认 模型 model default"
          env="ai.default_model"
          right={
            <>
              <Select
                size="sm"
                width={230}
                menuWidth={340}
                searchable
                searchPlaceholder="搜索模型"
                placeholder={models.loading ? "正在读取…" : models.error && !models.models.length ? "读不到模型列表" : "选择模型"}
                disabled={models.loading || !options.length}
                value={ai.default_model || ""}
                options={options}
                onChange={setDefault}
                ariaLabel="默认模型"
              />
              <IconButton icon="refresh" size="sm" label="重新读取模型列表" onClick={() => runTest()} />
            </>
          }
        />
        <SetRow
          title="连接测试"
          sub={test ? <TestResult test={test} /> : models.error ? <span className="set-test err">{models.error}</span> : "向模型来源要一次模型列表，看看地址和密钥是否可用"}
          keywords="测试 连接 test"
          right={
            <Button size="sm" icon="activity" loading={test?.state === "running"} onClick={runTest}>
              测试连接
            </Button>
          }
        />
      </List>
    </>
  );
}

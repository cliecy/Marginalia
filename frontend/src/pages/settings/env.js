/**
 * Settings that a `NOTES_*` environment variable decides at start-up.
 *
 * The Go server loads defaults → settings.json → environment (the environment
 * wins) and lists the variables it applied in `env_overrides` (on
 * /api/app/settings and /api/auth/me). A change made here still takes effect
 * at once, but the next start reads the variable again — the settings page
 * says so beside the row.
 */

/** Setting key ("section.field") → the variable that overrides it. */
export const ENV_VARS = {
  notes_dir: "NOTES_DIR",
  "ai.base_url": "NOTES_AI_BASE_URL",
  "ai.api_key": "NOTES_AI_API_KEY_FILE",
  "ai.default_model": "NOTES_AI_DEFAULT_MODEL",
  "git.autocommit": "NOTES_GIT_AUTOCOMMIT",
  "git.commit_delay_seconds": "NOTES_GIT_COMMIT_DELAY_SECONDS",
  "git.autosync_minutes": "NOTES_GIT_AUTOSYNC_MINUTES",
  "git.author_name": "NOTES_GIT_AUTHOR_NAME",
  "git.author_email": "NOTES_GIT_AUTHOR_EMAIL",
};

/**
 * The variable that overrides a setting in this run, or null.
 *
 * @param {{settings?: {env_overrides?: string[]}, me?: {env_overrides?: string[]}}} ctx the app context
 * @param {string} key a key of ENV_VARS
 * @returns {string|null}
 */
export function envOverride(ctx, key) {
  const name = ENV_VARS[key];
  if (!name) return null;
  const applied = ctx.settings?.env_overrides ?? ctx.me?.env_overrides ?? [];
  return applied.includes(name) ? name : null;
}

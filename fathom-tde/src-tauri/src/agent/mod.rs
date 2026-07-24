mod providers;
mod tools;

use tauri::AppHandle;

const KEYRING_SERVICE: &str = "com.giap.fathom-tde";
const MAX_ITERATIONS: usize = 8;

fn key_account(provider: &str) -> String {
    format!("agent:{provider}")
}

#[tauri::command]
pub fn agent_has_api_key(provider: String) -> bool {
    keyring::Entry::new(KEYRING_SERVICE, &key_account(&provider))
        .and_then(|e| e.get_password())
        .is_ok()
}

#[tauri::command]
pub fn agent_set_api_key(provider: String, key: String) -> Result<(), String> {
    let entry = keyring::Entry::new(KEYRING_SERVICE, &key_account(&provider)).map_err(|e| e.to_string())?;
    entry.set_password(&key).map_err(|e| e.to_string())
}

fn load_api_key(provider: &str) -> Result<String, String> {
    keyring::Entry::new(KEYRING_SERVICE, &key_account(provider))
        .map_err(|e| e.to_string())?
        .get_password()
        .map_err(|_| format!("No API key configured for {provider}"))
}

/// Self-correcting Rhai loop (research doc: "agent sửa code → validate → backtest lại → so metric
/// → lặp"). Stops when the model returns a final text-only response, or after `MAX_ITERATIONS` —
/// deliberately not a hardcoded quality/Sharpe gate, since the research doc leaves "what counts as
/// good enough" as an open, unresolved question; the model's own judgment drives the stop, not a
/// baked-in metric threshold.
#[tauri::command]
pub async fn agent_send(
    app: AppHandle,
    provider: String,
    project_path: String,
    message: String,
) -> Result<String, String> {
    let api_key = load_api_key(&provider)?;
    match provider.as_str() {
        "claude" => providers::run_claude(&app, &api_key, &project_path, &message).await,
        "openai" => providers::run_openai(&app, &api_key, &project_path, &message).await,
        "gemini" => providers::run_gemini(&app, &api_key, &project_path, &message).await,
        other => Err(format!("unknown provider: {other}")),
    }
}

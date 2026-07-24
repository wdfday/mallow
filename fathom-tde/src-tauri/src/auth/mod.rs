use std::sync::Mutex;

use serde::{Deserialize, Serialize};
use tauri::State;

pub mod google;

const KEYRING_SERVICE: &str = "com.giap.fathom-tde";
const REFRESH_TOKEN_ACCOUNT: &str = "refresh_token";

/// Default matches the Cloudflare tunnel's public route for the gateway (`~/.cloudflared/config.yml`:
/// `api.m4llow.com` → `http://gateway:8080`) — the same hosted endpoint mallow-client (the web
/// app) talks to, not a local-only address. Override with `FATHOM_GATEWAY_URL` for local dev
/// against a docker-compose gateway on `localhost:8080`.
pub(crate) fn gateway_url() -> String {
    std::env::var("FATHOM_GATEWAY_URL").unwrap_or_else(|_| "https://api.m4llow.com".to_string())
}

fn now_unix() -> i64 {
    chrono::Utc::now().timestamp()
}

// ── Types — field names copied verbatim from `auth-context.tsx`/`SettingsPanel.tsx`'s TS
// interfaces, which already match identity's JSON 1:1. ─────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuthUser {
    pub id: String,
    pub email: String,
    pub full_name: String,
    #[serde(default)]
    pub display_name: Option<String>,
    #[serde(default)]
    pub avatar_url: Option<String>,
    pub role: String,
    pub status: String,
    pub email_verified: bool,
    #[serde(default)]
    pub last_login_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuthToken {
    pub access_token: String,
    pub token_type: String,
    pub expires_in: i64,
    pub expires_at: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AuthSession {
    pub user: AuthUser,
    pub token: AuthToken,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionInfo {
    pub sid: String,
    pub ip: String,
    pub user_agent: String,
    pub created_at: String,
    pub expires_at: String,
    pub is_current: bool,
}

#[derive(Default)]
pub struct AuthState {
    session: Mutex<Option<AuthSession>>,
}

impl AuthState {
    pub fn new() -> Self {
        Self::default()
    }
}

// ── Wire-shape helpers (identity's raw JSON, before it becomes our normalized types) ────────────

#[derive(Deserialize)]
struct RawToken {
    access_token: String,
    token_type: String,
    expires_in: i64,
    #[serde(default)]
    expires_at: i64,
}

#[derive(Deserialize)]
struct LoginData {
    user: AuthUser,
    token: RawToken,
}

#[derive(Deserialize)]
struct Envelope<T> {
    message: Option<String>,
    data: Option<T>,
}

fn build_token(raw: RawToken) -> AuthToken {
    let expires_at = if raw.expires_at > 0 { raw.expires_at } else { now_unix() + raw.expires_in };
    AuthToken { access_token: raw.access_token, token_type: raw.token_type, expires_in: raw.expires_in, expires_at }
}

fn extract_refresh_cookie(res: &reqwest::Response) -> Option<String> {
    for value in res.headers().get_all(reqwest::header::SET_COOKIE) {
        let s = value.to_str().ok()?;
        if let Some(rest) = s.strip_prefix("refresh_token=") {
            let val = rest.split(';').next().unwrap_or("").to_string();
            if !val.is_empty() {
                return Some(val);
            }
        }
    }
    None
}

async fn parse_envelope<T: for<'de> Deserialize<'de>>(res: reqwest::Response) -> Result<T, String> {
    let status = res.status();
    let bytes = res.bytes().await.map_err(|e| e.to_string())?;
    if !status.is_success() {
        let msg = serde_json::from_slice::<serde_json::Value>(&bytes)
            .ok()
            .and_then(|v| v.get("message").and_then(|m| m.as_str()).map(|s| s.to_string()))
            .unwrap_or_else(|| format!("request failed ({status})"));
        return Err(msg);
    }
    let envelope: Envelope<T> = serde_json::from_slice(&bytes).map_err(|e| e.to_string())?;
    envelope.data.ok_or_else(|| envelope.message.unwrap_or_else(|| "empty response".to_string()))
}

/// For endpoints whose success response carries no `data` (e.g. "session revoked") — only the
/// HTTP status / `message` matter, an absent `data` field is not an error here.
async fn parse_ok(res: reqwest::Response) -> Result<(), String> {
    let status = res.status();
    let bytes = res.bytes().await.map_err(|e| e.to_string())?;
    if status.is_success() {
        return Ok(());
    }
    let msg = serde_json::from_slice::<serde_json::Value>(&bytes)
        .ok()
        .and_then(|v| v.get("message").and_then(|m| m.as_str()).map(|s| s.to_string()))
        .unwrap_or_else(|| format!("request failed ({status})"));
    Err(msg)
}

fn save_refresh_token(value: &str) -> Result<(), String> {
    let entry = keyring::Entry::new(KEYRING_SERVICE, REFRESH_TOKEN_ACCOUNT).map_err(|e| e.to_string())?;
    entry.set_password(value).map_err(|e| e.to_string())
}

fn load_refresh_token() -> Option<String> {
    let entry = keyring::Entry::new(KEYRING_SERVICE, REFRESH_TOKEN_ACCOUNT).ok()?;
    entry.get_password().ok()
}

fn clear_refresh_token() {
    if let Ok(entry) = keyring::Entry::new(KEYRING_SERVICE, REFRESH_TOKEN_ACCOUNT) {
        let _ = entry.delete_credential();
    }
}

/// `GET /api/v1/user/me` — the only way to recover the user object from a bare access token;
/// `/auth/refresh` intentionally returns token fields only (see identity's `TokenResponse`).
async fn fetch_current_user(client: &reqwest::Client, access_token: &str) -> Result<AuthUser, String> {
    let res = client
        .get(format!("{}/api/v1/user/me", gateway_url()))
        .bearer_auth(access_token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    parse_envelope(res).await
}

fn set_session(state: &AuthState, session: AuthSession) -> Result<(), String> {
    *state.session.lock().map_err(|_| "auth state poisoned")? = Some(session);
    Ok(())
}

pub(crate) fn current_access_token(state: &AuthState) -> Result<String, String> {
    let guard = state.session.lock().map_err(|_| "auth state poisoned")?;
    guard.as_ref().map(|s| s.token.access_token.clone()).ok_or_else(|| "not authenticated".to_string())
}

/// `(access_token, expires_at)` — the WS stream module uses `expires_at` to refresh proactively
/// before dialing, since (unlike an HTTP call) a WS handshake gets no per-message retry-on-401.
pub(crate) fn current_token_and_expiry(state: &AuthState) -> Result<(String, i64), String> {
    let guard = state.session.lock().map_err(|_| "auth state poisoned")?;
    guard
        .as_ref()
        .map(|s| (s.token.access_token.clone(), s.token.expires_at))
        .ok_or_else(|| "not authenticated".to_string())
}

// ── Commands ─────────────────────────────────────────────────────────────────────────────────

#[tauri::command]
pub async fn auth_login(
    state: State<'_, AuthState>,
    email: String,
    password: String,
) -> Result<AuthSession, String> {
    let client = reqwest::Client::new();
    let res = client
        .post(format!("{}/api/v1/auth/login", gateway_url()))
        .json(&serde_json::json!({ "email": email, "password": password }))
        .send()
        .await
        .map_err(|e| e.to_string())?;

    let refresh_cookie = extract_refresh_cookie(&res);
    let data: LoginData = parse_envelope(res).await?;
    if let Some(cookie) = refresh_cookie {
        save_refresh_token(&cookie)?;
    }
    let session = AuthSession { user: data.user, token: build_token(data.token) };
    set_session(&state, session.clone())?;
    Ok(session)
}

/// Shared by the `auth_refresh` command and `gateway_fetch`'s own retry-on-401 path — factored
/// out so a generic resource-API call can refresh the session exactly the same way the explicit
/// "refresh" command does, without going through `tauri::command`'s invoke machinery internally.
pub(crate) async fn refresh_session(state: &State<'_, AuthState>) -> Result<AuthSession, String> {
    let cookie = load_refresh_token().ok_or("not logged in")?;
    let client = reqwest::Client::new();
    let res = client
        .post(format!("{}/api/v1/auth/refresh", gateway_url()))
        .header(reqwest::header::COOKIE, format!("refresh_token={cookie}"))
        .send()
        .await
        .map_err(|e| e.to_string())?;

    let refreshed_cookie = extract_refresh_cookie(&res);
    let raw: RawToken = parse_envelope(res).await?;
    if let Some(c) = refreshed_cookie {
        save_refresh_token(&c)?;
    }
    let token = build_token(raw);

    // Cheap path: reuse the cached user if we have one (the common case — an access token expired
    // mid-session, the user hasn't changed). Cold start (no cached session yet) falls back to
    // `/user/me` since `/auth/refresh` never returns a user object.
    let cached_user = state.session.lock().map_err(|_| "auth state poisoned")?.as_ref().map(|s| s.user.clone());
    let user = match cached_user {
        Some(u) => u,
        None => fetch_current_user(&client, &token.access_token).await?,
    };

    let session = AuthSession { user, token };
    set_session(state, session.clone())?;
    Ok(session)
}

#[tauri::command]
pub async fn auth_refresh(state: State<'_, AuthState>) -> Result<AuthSession, String> {
    refresh_session(&state).await
}

/// Generic hosted-resource call (helm/broker/hand/herald/…) — the Rust-side counterpart of the
/// old TS `apiFetch`: Bearer auth from the current session, envelope unwrap, one retry through
/// `refresh_session` on a 401. Every network call in the app goes through Rust now (matching
/// `auth_login`/`auth_refresh`'s own `reqwest` calls above) — the frontend's `apiFetch`
/// (auth-context.tsx) is a thin `invoke('gateway_fetch', …)` wrapper, and every business panel
/// (Helm/Broker/Hand views, herald chart-data fallback) calls THAT, unchanged — they only ever
/// depended on `apiFetch`'s `(path, init) => Promise<T>` shape, never on how it reaches the
/// network, so none of those call sites needed to change for this to move server-side into Rust.
#[tauri::command]
pub async fn gateway_fetch(
    state: State<'_, AuthState>,
    path: String,
    method: String,
    body: Option<serde_json::Value>,
) -> Result<serde_json::Value, String> {
    let http_method = reqwest::Method::from_bytes(method.as_bytes()).map_err(|_| format!("invalid method: {method}"))?;
    let url = format!("{}{}", gateway_url(), path);
    let client = reqwest::Client::new();

    async fn send(
        client: &reqwest::Client,
        method: &reqwest::Method,
        url: &str,
        token: &str,
        body: &Option<serde_json::Value>,
    ) -> Result<reqwest::Response, String> {
        let mut req = client.request(method.clone(), url).bearer_auth(token);
        if let Some(b) = body {
            req = req.json(b);
        }
        req.send().await.map_err(|e| e.to_string())
    }

    let access_token = match current_access_token(&state) {
        Ok(t) => t,
        Err(e) => {
            // The one case that looks identical to a network failure from the caller's side but
            // has a completely different fix (log in) — worth its own line rather than getting
            // lost inside whatever catch-all error handling the TS call site does.
            eprintln!("[gateway_fetch] {method} {path} — no session: {e}");
            return Err(e);
        }
    };
    // Body summary only for the data endpoint — this is specifically to watch the `before`
    // cursor advance as the chart's "load back" scroll handler pages through history; every other
    // resource call's body is uninteresting noise here.
    if path.starts_with("/api/v1/data/") {
        let cursor = body.as_ref().and_then(|b| b.get("candles")).map(|c| c.to_string()).unwrap_or_default();
        eprintln!("[gateway_fetch] {method} {path} candles={cursor}");
    }
    let mut res = send(&client, &http_method, &url, &access_token, &body).await?;
    eprintln!("[gateway_fetch] {method} {path} -> {}", res.status());

    if res.status() == reqwest::StatusCode::UNAUTHORIZED {
        if let Ok(session) = refresh_session(&state).await {
            res = send(&client, &http_method, &url, &session.token.access_token, &body).await?;
            eprintln!("[gateway_fetch] {method} {path} retried after refresh -> {}", res.status());
        }
    }

    parse_envelope(res).await
}

#[tauri::command]
pub async fn auth_current_session(state: State<'_, AuthState>) -> Result<Option<AuthSession>, String> {
    if load_refresh_token().is_none() {
        return Ok(None);
    }
    match auth_refresh(state).await {
        Ok(session) => Ok(Some(session)),
        // A stale/expired/revoked refresh token shouldn't surface as an error toast — it just
        // means "not logged in", same as if the cookie had never existed.
        Err(_) => {
            clear_refresh_token();
            Ok(None)
        }
    }
}

#[tauri::command]
pub async fn auth_logout(state: State<'_, AuthState>) -> Result<(), String> {
    let access_token = state.session.lock().map_err(|_| "auth state poisoned")?.as_ref().map(|s| s.token.access_token.clone());
    let cookie = load_refresh_token();

    if let (Some(access_token), Some(cookie)) = (access_token, cookie) {
        let client = reqwest::Client::new();
        let _ = client
            .post(format!("{}/api/v1/auth/logout", gateway_url()))
            .bearer_auth(access_token)
            .header(reqwest::header::COOKIE, format!("refresh_token={cookie}"))
            .send()
            .await;
    }

    clear_refresh_token();
    *state.session.lock().map_err(|_| "auth state poisoned")? = None;
    Ok(())
}

#[tauri::command]
pub async fn auth_list_sessions(state: State<'_, AuthState>) -> Result<Vec<SessionInfo>, String> {
    let access_token = current_access_token(&state)?;
    let client = reqwest::Client::new();
    let res = client
        .get(format!("{}/api/v1/auth/sessions", gateway_url()))
        .bearer_auth(access_token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    parse_envelope(res).await
}

#[tauri::command]
pub async fn auth_revoke_session(state: State<'_, AuthState>, sid: String) -> Result<(), String> {
    let access_token = current_access_token(&state)?;
    let client = reqwest::Client::new();
    let res = client
        .delete(format!("{}/api/v1/auth/sessions/{sid}", gateway_url()))
        .bearer_auth(access_token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    parse_ok(res).await
}

#[tauri::command]
pub async fn auth_revoke_all_sessions(state: State<'_, AuthState>) -> Result<(), String> {
    let access_token = current_access_token(&state)?;
    let client = reqwest::Client::new();
    let res = client
        .delete(format!("{}/api/v1/auth/sessions", gateway_url()))
        .bearer_auth(access_token)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    parse_ok(res).await
}

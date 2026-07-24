use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine as _};
use rand::RngCore;
use serde::Deserialize;
use sha2::{Digest, Sha256};
use tauri::{AppHandle, State};
use tauri_plugin_opener::OpenerExt;

use super::{
    build_token, extract_refresh_cookie, gateway_url, parse_envelope, save_refresh_token, set_session, AuthSession,
    AuthState, LoginData,
};

/// Identity has no redirect/callback handling of its own (verified — its `/auth/google` is a
/// single-shot ID-token exchange, see `mod.rs`'s comment). A desktop client has to run its own
/// PKCE + loopback-redirect flow against Google directly to get that ID token first.
fn gen_pkce() -> (String, String) {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
    let verifier = URL_SAFE_NO_PAD.encode(bytes);
    let challenge = URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()));
    (verifier, challenge)
}

fn extract_query_param(url: &str, key: &str) -> Option<String> {
    let query = url.split_once('?')?.1;
    for pair in query.split('&') {
        let (k, v) = pair.split_once('=')?;
        if k == key {
            return Some(urlencoding::decode(v).ok()?.into_owned());
        }
    }
    None
}

#[derive(Deserialize)]
struct GoogleTokenResponse {
    id_token: String,
}

#[tauri::command]
pub async fn auth_google_start(app: AppHandle, state: State<'_, AuthState>) -> Result<AuthSession, String> {
    let client_id = std::env::var("FATHOM_GOOGLE_CLIENT_ID")
        .map_err(|_| "Google login is not configured (missing FATHOM_GOOGLE_CLIENT_ID)".to_string())?;

    let (verifier, challenge) = gen_pkce();

    let server = tiny_http::Server::http("127.0.0.1:0").map_err(|e| e.to_string())?;
    let port = match server.server_addr() {
        tiny_http::ListenAddr::IP(addr) => addr.port(),
        _ => return Err("unexpected loopback listener address".to_string()),
    };
    let redirect_uri = format!("http://127.0.0.1:{port}");

    let auth_url = format!(
        "https://accounts.google.com/o/oauth2/v2/auth?client_id={}&redirect_uri={}&response_type=code&scope={}&code_challenge={}&code_challenge_method=S256&access_type=online",
        urlencoding::encode(&client_id),
        urlencoding::encode(&redirect_uri),
        urlencoding::encode("openid email profile"),
        urlencoding::encode(&challenge),
    );

    app.opener().open_url(auth_url, None::<&str>).map_err(|e| e.to_string())?;

    // `tiny_http::Server::recv` blocks the calling thread until a request arrives — run it on a
    // blocking-pool thread so it doesn't stall the async runtime while the user is in the browser.
    let code = tokio::task::spawn_blocking(move || -> Result<String, String> {
        let request = server.recv().map_err(|e| e.to_string())?;
        let url = request.url().to_string();
        let code = extract_query_param(&url, "code");
        let body = if code.is_some() {
            "<html><body>Signed in with Google — you can return to fathom-tde.</body></html>"
        } else {
            "<html><body>Google sign-in failed — no authorization code received.</body></html>"
        };
        let header = tiny_http::Header::from_bytes(&b"Content-Type"[..], &b"text/html; charset=utf-8"[..])
            .expect("static header is valid");
        let _ = request.respond(tiny_http::Response::from_string(body).with_header(header));
        code.ok_or_else(|| "no authorization code in Google's redirect".to_string())
    })
    .await
    .map_err(|e| e.to_string())??;

    let client = reqwest::Client::new();
    let token_res = client
        .post("https://oauth2.googleapis.com/token")
        .form(&[
            ("client_id", client_id.as_str()),
            ("code", code.as_str()),
            ("code_verifier", verifier.as_str()),
            ("grant_type", "authorization_code"),
            ("redirect_uri", redirect_uri.as_str()),
        ])
        .send()
        .await
        .map_err(|e| e.to_string())?;

    if !token_res.status().is_success() {
        let body = token_res.text().await.unwrap_or_default();
        return Err(format!("Google token exchange failed: {body}"));
    }
    let google_tokens: GoogleTokenResponse = token_res.json().await.map_err(|e| e.to_string())?;

    let res = client
        .post(format!("{}/api/v1/auth/google", gateway_url()))
        .json(&serde_json::json!({ "token": google_tokens.id_token }))
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

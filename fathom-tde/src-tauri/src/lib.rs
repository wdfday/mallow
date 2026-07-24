mod agent;
mod auth;
mod data;
mod search;
mod stream;
mod terminal;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_fs::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .manage(auth::AuthState::new())
        .manage(terminal::TerminalState::new())
        .manage(stream::StreamState::new())
        .setup(|_app| {
            // ~/Fathom/{.data,.agent} + SQLite schema. A failure (exotic fs setup) degrades
            // data features but must not stop the app from opening.
            if let Err(e) = data::home::bootstrap() {
                eprintln!("fathom home bootstrap failed: {e}");
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            auth::auth_login,
            auth::google::auth_google_start,
            auth::auth_refresh,
            auth::auth_logout,
            auth::auth_current_session,
            auth::auth_list_sessions,
            auth::auth_revoke_session,
            auth::auth_revoke_all_sessions,
            auth::gateway_fetch,
            agent::agent_has_api_key,
            agent::agent_set_api_key,
            agent::agent_send,
            terminal::terminal_spawn,
            terminal::terminal_write,
            terminal::terminal_resize,
            terminal::terminal_kill,
            data::load_ohlcv,
            data::load_ohlcv_csv,
            data::load_bars,
            data::home::fathom_home_path,
            data::registry::sources_list,
            data::registry::sources_add_mount,
            data::registry::sources_remove,
            data::registry::symbols_list,
            data::loader::symbols_add,
            data::loader::symbols_remove,
            data::catalog::data_catalog,
            data::backtest::backtest_run,
            search::search_project,
            stream::stream_connect,
            stream::stream_subscribe_bars,
            stream::stream_unsubscribe_bars,
            stream::stream_disconnect,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}

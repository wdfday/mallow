use std::collections::HashMap;
use std::io::{Read, Write};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Mutex;

use portable_pty::{native_pty_system, Child, CommandBuilder, MasterPty, PtySize};
use tauri::{AppHandle, Emitter, State};

/// Real PTY (portable-pty, not a log viewer) — one shell session per active project, rooted at
/// its directory. See `CliPanel.tsx`'s comment for the exact contract this mirrors.
struct PtySession {
    writer: Box<dyn Write + Send>,
    master: Box<dyn MasterPty + Send>,
    child: Box<dyn Child + Send + Sync>,
}

#[derive(Default)]
pub struct TerminalState {
    sessions: Mutex<HashMap<u32, PtySession>>,
}

impl TerminalState {
    pub fn new() -> Self {
        Self::default()
    }
}

static NEXT_ID: AtomicU32 = AtomicU32::new(1);

fn default_shell() -> String {
    std::env::var("SHELL").unwrap_or_else(|_| "/bin/zsh".to_string())
}

#[tauri::command]
pub fn terminal_spawn(
    app: AppHandle,
    state: State<TerminalState>,
    cwd: String,
    rows: u16,
    cols: u16,
) -> Result<u32, String> {
    let pty_system = native_pty_system();
    let pair = pty_system
        .openpty(PtySize { rows, cols, pixel_width: 0, pixel_height: 0 })
        .map_err(|e| e.to_string())?;

    let mut cmd = CommandBuilder::new(default_shell());
    cmd.cwd(&cwd);

    let child = pair.slave.spawn_command(cmd).map_err(|e| e.to_string())?;
    drop(pair.slave); // only the master side is needed after the child is spawned

    let mut reader = pair.master.try_clone_reader().map_err(|e| e.to_string())?;
    let writer = pair.master.take_writer().map_err(|e| e.to_string())?;

    let id = NEXT_ID.fetch_add(1, Ordering::SeqCst);

    {
        let mut sessions = state.sessions.lock().map_err(|_| "terminal state poisoned")?;
        sessions.insert(id, PtySession { writer, master: pair.master, child });
    }

    // Reader thread: pushes raw PTY output to the frontend as it arrives.
    let app_for_reader = app.clone();
    std::thread::spawn(move || {
        let mut buf = [0u8; 4096];
        loop {
            match reader.read(&mut buf) {
                Ok(0) => break,
                Ok(n) => {
                    let chunk = String::from_utf8_lossy(&buf[..n]).to_string();
                    let _ = app_for_reader.emit(&format!("terminal://output/{id}"), chunk);
                }
                Err(_) => break,
            }
        }
        let _ = app_for_reader.emit(&format!("terminal://exit/{id}"), ());
    });

    Ok(id)
}

#[tauri::command]
pub fn terminal_write(state: State<TerminalState>, id: u32, data: String) -> Result<(), String> {
    let mut sessions = state.sessions.lock().map_err(|_| "terminal state poisoned")?;
    let session = sessions.get_mut(&id).ok_or("no such terminal session")?;
    session.writer.write_all(data.as_bytes()).map_err(|e| e.to_string())
}

#[tauri::command]
pub fn terminal_resize(state: State<TerminalState>, id: u32, rows: u16, cols: u16) -> Result<(), String> {
    let sessions = state.sessions.lock().map_err(|_| "terminal state poisoned")?;
    let session = sessions.get(&id).ok_or("no such terminal session")?;
    session
        .master
        .resize(PtySize { rows, cols, pixel_width: 0, pixel_height: 0 })
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub fn terminal_kill(state: State<TerminalState>, id: u32) -> Result<(), String> {
    let mut sessions = state.sessions.lock().map_err(|_| "terminal state poisoned")?;
    if let Some(mut session) = sessions.remove(&id) {
        let _ = session.child.kill();
    }
    Ok(())
}

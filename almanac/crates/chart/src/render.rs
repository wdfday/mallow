//! Output helpers — write HTML / PNG / SVG to disk and open in browser.

use anyhow::Result;
use std::path::{Path, PathBuf};

/// Write an HTML string to a file, creating parent directories if needed.
pub fn write_html(html: &str, path: impl AsRef<Path>) -> Result<PathBuf> {
    let path = path.as_ref();
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    std::fs::write(path, html)?;
    Ok(path.to_path_buf())
}

/// Write HTML and attempt to open it in the default browser (best-effort).
pub fn open_html(html: &str, path: impl AsRef<Path>) -> Result<PathBuf> {
    let p = write_html(html, &path)?;
    let _ = open_in_browser(&p); // ignore error — user can open manually
    Ok(p)
}

/// Default output directory for debug charts: `debug/charts/`.
pub fn default_output_dir() -> PathBuf {
    PathBuf::from("debug/charts")
}

/// Construct a debug file path: `debug/charts/<name>.html`
pub fn debug_html_path(name: &str) -> PathBuf {
    default_output_dir().join(format!("{name}.html"))
}

/// Construct a debug file path: `debug/charts/<name>.png`
pub fn debug_png_path(name: &str) -> PathBuf {
    default_output_dir().join(format!("{name}.png"))
}

/// Construct a debug file path: `debug/charts/<name>.svg`
pub fn debug_svg_path(name: &str) -> PathBuf {
    default_output_dir().join(format!("{name}.svg"))
}

fn open_in_browser(path: &Path) -> Result<()> {
    let abs = path
        .canonicalize()
        .unwrap_or_else(|_| path.to_path_buf());

    #[cfg(target_os = "macos")]
    std::process::Command::new("open").arg(&abs).spawn()?;
    #[cfg(target_os = "linux")]
    std::process::Command::new("xdg-open").arg(&abs).spawn()?;
    #[cfg(target_os = "windows")]
    std::process::Command::new("cmd")
        .args(["/c", "start", abs.to_str().unwrap_or("")])
        .spawn()?;

    Ok(())
}

use std::path::Path;

use serde::Serialize;

/// Plain substring search over a project's text files — no regex/fuzzy engine, matches a
/// research-project-sized directory (see `SearchPanel.tsx`'s comment).
const SKIP_DIRS: &[&str] = &["node_modules", ".git", "target", "dist", ".venv", "__pycache__"];
const MAX_HITS: usize = 200;
const MAX_FILE_BYTES: u64 = 2_000_000; // skip anything that isn't obviously a small text file

#[derive(Debug, Clone, Serialize)]
pub struct SearchHit {
    pub file: String,
    pub line: usize,
    pub text: String,
}

#[tauri::command]
pub fn search_project(project_path: String, query: String) -> Result<Vec<SearchHit>, String> {
    let root = Path::new(&project_path);
    if query.trim().is_empty() {
        return Ok(Vec::new());
    }
    let query_lower = query.to_lowercase();
    let mut hits = Vec::new();
    walk(root, root, &query_lower, &mut hits);
    Ok(hits)
}

fn walk(root: &Path, dir: &Path, query_lower: &str, hits: &mut Vec<SearchHit>) {
    if hits.len() >= MAX_HITS {
        return;
    }
    let Ok(entries) = std::fs::read_dir(dir) else { return };
    for entry in entries.flatten() {
        if hits.len() >= MAX_HITS {
            return;
        }
        let path = entry.path();
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if path.is_dir() {
            if SKIP_DIRS.contains(&name.as_ref()) || name.starts_with('.') {
                continue;
            }
            walk(root, &path, query_lower, hits);
            continue;
        }
        search_file(root, &path, query_lower, hits);
    }
}

fn search_file(root: &Path, path: &Path, query_lower: &str, hits: &mut Vec<SearchHit>) {
    let Ok(meta) = std::fs::metadata(path) else { return };
    if meta.len() > MAX_FILE_BYTES {
        return;
    }
    let Ok(content) = std::fs::read_to_string(path) else { return }; // binary/non-utf8 files fail silently, same as skipping them
    let rel = path.strip_prefix(root).unwrap_or(path).to_string_lossy().to_string();
    for (i, line) in content.lines().enumerate() {
        if hits.len() >= MAX_HITS {
            return;
        }
        if line.to_lowercase().contains(query_lower) {
            hits.push(SearchHit { file: rel.clone(), line: i + 1, text: line.trim().to_string() });
        }
    }
}

use serde_json::{json, Value};
use tauri::{AppHandle, Emitter};

use super::{tools, MAX_ITERATIONS};

fn emit_step(app: &AppHandle, kind: &str, name: &str, args: Option<Value>, ok: Option<bool>, summary: Option<String>) {
    let _ = app.emit(
        "agent://step",
        json!({ "type": kind, "name": name, "args": args, "ok": ok, "summary": summary }),
    );
}

fn run_tool(app: &AppHandle, name: &str, args: &Value, project_path: &str) -> Result<Value, String> {
    emit_step(app, "tool_call", name, Some(args.clone()), None, None);
    let result = tools::execute(name, args, project_path);
    let (ok, summary) = match &result {
        Ok(v) => (true, tools::summarize(name, v)),
        Err(e) => (false, e.clone()),
    };
    emit_step(app, "tool_result", name, None, Some(ok), Some(summary));
    result
}

// ── Claude (Anthropic Messages API, tool_use blocks) ────────────────────────────────────────────

pub async fn run_claude(app: &AppHandle, api_key: &str, project_path: &str, message: &str) -> Result<String, String> {
    let client = reqwest::Client::new();
    let tools_json: Vec<Value> = tools::tool_specs()
        .into_iter()
        .map(|t| json!({ "name": t["name"], "description": t["description"], "input_schema": t["parameters"] }))
        .collect();

    let mut messages = vec![json!({ "role": "user", "content": message })];
    let model = std::env::var("FATHOM_CLAUDE_MODEL").unwrap_or_else(|_| "claude-sonnet-5".to_string());

    for _ in 0..MAX_ITERATIONS {
        let res = client
            .post("https://api.anthropic.com/v1/messages")
            .header("x-api-key", api_key)
            .header("anthropic-version", "2023-06-01")
            .json(&json!({ "model": model, "max_tokens": 4096, "messages": messages, "tools": tools_json }))
            .send()
            .await
            .map_err(|e| e.to_string())?;

        let status = res.status();
        let body: Value = res.json().await.map_err(|e| e.to_string())?;
        if !status.is_success() {
            let msg = body.pointer("/error/message").and_then(|m| m.as_str()).unwrap_or("Claude API error");
            return Err(msg.to_string());
        }

        let content = body.get("content").and_then(|c| c.as_array()).cloned().unwrap_or_default();
        messages.push(json!({ "role": "assistant", "content": content }));

        let tool_uses: Vec<&Value> = content.iter().filter(|b| b["type"] == "tool_use").collect();
        if tool_uses.is_empty() {
            let text = content.iter().find(|b| b["type"] == "text").and_then(|b| b["text"].as_str()).unwrap_or("");
            return Ok(text.to_string());
        }

        let mut tool_results = Vec::new();
        for call in tool_uses {
            let name = call["name"].as_str().unwrap_or_default().to_string();
            let args = call["input"].clone();
            let id = call["id"].as_str().unwrap_or_default().to_string();
            let result = run_tool(app, &name, &args, project_path);
            let content_str = match result {
                Ok(v) => v.to_string(),
                Err(e) => json!({ "error": e }).to_string(),
            };
            tool_results.push(json!({ "type": "tool_result", "tool_use_id": id, "content": content_str }));
        }
        messages.push(json!({ "role": "user", "content": tool_results }));
    }
    Err("agent stopped after too many iterations without a final answer".to_string())
}

// ── OpenAI (Chat Completions, function-calling tools) ────────────────────────────────────────────

pub async fn run_openai(app: &AppHandle, api_key: &str, project_path: &str, message: &str) -> Result<String, String> {
    let client = reqwest::Client::new();
    let tools_json: Vec<Value> = tools::tool_specs()
        .into_iter()
        .map(|t| json!({ "type": "function", "function": { "name": t["name"], "description": t["description"], "parameters": t["parameters"] } }))
        .collect();

    let mut messages = vec![json!({ "role": "user", "content": message })];
    let model = std::env::var("FATHOM_OPENAI_MODEL").unwrap_or_else(|_| "gpt-4o".to_string());

    for _ in 0..MAX_ITERATIONS {
        let res = client
            .post("https://api.openai.com/v1/chat/completions")
            .bearer_auth(api_key)
            .json(&json!({ "model": model, "messages": messages, "tools": tools_json }))
            .send()
            .await
            .map_err(|e| e.to_string())?;

        let status = res.status();
        let body: Value = res.json().await.map_err(|e| e.to_string())?;
        if !status.is_success() {
            let msg = body.pointer("/error/message").and_then(|m| m.as_str()).unwrap_or("OpenAI API error");
            return Err(msg.to_string());
        }

        let choice = body.pointer("/choices/0/message").cloned().unwrap_or(Value::Null);
        messages.push(choice.clone());

        let tool_calls: Vec<Value> = choice.get("tool_calls").and_then(|c| c.as_array()).cloned().unwrap_or_default();
        if tool_calls.is_empty() {
            let text = choice.get("content").and_then(|c| c.as_str()).unwrap_or("");
            return Ok(text.to_string());
        }

        for call in tool_calls {
            let id = call["id"].as_str().unwrap_or_default().to_string();
            let name = call.pointer("/function/name").and_then(|n| n.as_str()).unwrap_or_default().to_string();
            let args: Value = call
                .pointer("/function/arguments")
                .and_then(|a| a.as_str())
                .and_then(|s| serde_json::from_str(s).ok())
                .unwrap_or(json!({}));

            let result = run_tool(app, &name, &args, project_path);
            let content_str = match result {
                Ok(v) => v.to_string(),
                Err(e) => json!({ "error": e }).to_string(),
            };
            messages.push(json!({ "role": "tool", "tool_call_id": id, "content": content_str }));
        }
    }
    Err("agent stopped after too many iterations without a final answer".to_string())
}

// ── Gemini (generateContent, function-calling) ───────────────────────────────────────────────────

pub async fn run_gemini(app: &AppHandle, api_key: &str, project_path: &str, message: &str) -> Result<String, String> {
    let client = reqwest::Client::new();
    let function_decls: Vec<Value> = tools::tool_specs()
        .into_iter()
        .map(|t| json!({ "name": t["name"], "description": t["description"], "parameters": t["parameters"] }))
        .collect();

    let mut contents = vec![json!({ "role": "user", "parts": [{ "text": message }] })];
    let model = std::env::var("FATHOM_GEMINI_MODEL").unwrap_or_else(|_| "gemini-2.0-flash".to_string());

    for _ in 0..MAX_ITERATIONS {
        let url = format!("https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent?key={api_key}");
        let res = client
            .post(url)
            .json(&json!({ "contents": contents, "tools": [{ "function_declarations": function_decls }] }))
            .send()
            .await
            .map_err(|e| e.to_string())?;

        let status = res.status();
        let body: Value = res.json().await.map_err(|e| e.to_string())?;
        if !status.is_success() {
            let msg = body.pointer("/error/message").and_then(|m| m.as_str()).unwrap_or("Gemini API error");
            return Err(msg.to_string());
        }

        let content = body.pointer("/candidates/0/content").cloned().unwrap_or(json!({ "role": "model", "parts": [] }));
        let parts = content.get("parts").and_then(|p| p.as_array()).cloned().unwrap_or_default();
        contents.push(json!({ "role": "model", "parts": parts }));

        let calls: Vec<&Value> = parts.iter().filter(|p| p.get("functionCall").is_some()).collect();
        if calls.is_empty() {
            let text = parts.iter().find_map(|p| p.get("text")).and_then(|t| t.as_str()).unwrap_or("");
            return Ok(text.to_string());
        }

        let mut response_parts = Vec::new();
        for part in calls {
            let call = &part["functionCall"];
            let name = call["name"].as_str().unwrap_or_default().to_string();
            let args = call.get("args").cloned().unwrap_or(json!({}));
            let result = run_tool(app, &name, &args, project_path);
            let response_value = match result {
                Ok(v) => v,
                Err(e) => json!({ "error": e }),
            };
            response_parts.push(json!({ "functionResponse": { "name": name, "response": { "result": response_value } } }));
        }
        contents.push(json!({ "role": "user", "parts": response_parts }));
    }
    Err("agent stopped after too many iterations without a final answer".to_string())
}

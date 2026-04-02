//! Bot registry — multi-bot, multi-symbol signal engine.
//!
//! Architecture:
//!   Registry
//!     └─ SymbolGroup (one per symbol)
//!          ├─ bar_window: Vec<Bar>   (shared sliding window, capped at MAX_WINDOW)
//!          └─ BotHandle (one per registered bot)
//!               └─ Box<dyn Strategy>  (stateful, updated every bar)
//!
//! When a bar arrives for symbol S, Registry finds SymbolGroup(S) and:
//!   1. Appends bar to the shared window (max MAX_WINDOW bars kept).
//!   2. Calls each bot's strategy.on_bar(bar)  — standard per-bar signal.
//!   3. Calls each bot's strategy.on_window(&window) — pattern/multi-bar signal.
//!   4. Merges both signal sets and tags results with bot_id.
//!
//! Deduplication: all bots watching the same symbol share ONE bar window and
//! ONE bar fan-out; indicator state is per-bot (each Box<dyn Strategy> owns its own).

use anyhow::{anyhow, Result};
use alm_core::{signal::Signal, strategy::Strategy, Bar};
use alm_strategy::factory::{build_strategy, params_from_map};
use std::collections::HashMap;

const MAX_WINDOW: usize = 200;

// ── BotHandle ─────────────────────────────────────────────────────────────────

pub struct BotHandle {
    pub bot_id: String,
    pub symbol: String,
    pub strategy_name: String,
    pub params_json: String,
    strategy: Box<dyn Strategy>,
}

impl BotHandle {
    fn new(
        bot_id: String,
        symbol: String,
        strategy_name: String,
        params_json: String,
    ) -> Result<Self> {
        let strategy = build_from_json(&strategy_name, &params_json)?;
        Ok(Self {
            bot_id,
            symbol,
            strategy_name,
            params_json,
            strategy,
        })
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.strategy.on_bar(bar)
    }

    fn on_window(&mut self, bars: &[Bar]) -> Vec<Signal> {
        self.strategy.on_window(bars)
    }
}

// ── SymbolGroup ───────────────────────────────────────────────────────────────

/// All bots watching the same symbol. Bar is fan-out once per bar arrival.
pub struct SymbolGroup {
    #[allow(dead_code)]
    pub symbol: String,
    bots: Vec<BotHandle>,
    /// Shared sliding window of recent bars for this symbol.
    /// Passed to strategy.on_window() for pattern-detection strategies.
    bar_window: Vec<Bar>,
}

impl SymbolGroup {
    fn new(symbol: String) -> Self {
        Self {
            symbol,
            bots: Vec::new(),
            bar_window: Vec::new(),
        }
    }

    fn add(&mut self, bot: BotHandle) {
        // Replace existing bot with same id (re-register = reconfigure)
        self.bots.retain(|b| b.bot_id != bot.bot_id);
        self.bots.push(bot);
    }

    fn remove(&mut self, bot_id: &str) {
        self.bots.retain(|b| b.bot_id != bot_id);
    }

    pub fn is_empty(&self) -> bool {
        self.bots.is_empty()
    }

    /// Returns (bot_id, signals) for each bot that emitted at least one signal.
    /// Signals from on_bar() and on_window() are merged per bot.
    pub fn on_bar(&mut self, bar: &Bar) -> Vec<(String, Vec<Signal>)> {
        // Advance shared window
        self.bar_window.push(bar.clone());
        if self.bar_window.len() > MAX_WINDOW {
            self.bar_window.remove(0);
        }

        self.bots
            .iter_mut()
            .filter_map(|b| {
                let mut sigs = b.on_bar(bar);
                sigs.extend(b.on_window(&self.bar_window));
                if sigs.is_empty() {
                    None
                } else {
                    Some((b.bot_id.clone(), sigs))
                }
            })
            .collect()
    }

    pub fn bot_infos(&self) -> impl Iterator<Item = &BotHandle> {
        self.bots.iter()
    }
}

// ── Registry ──────────────────────────────────────────────────────────────────

pub struct Registry {
    groups: HashMap<String, SymbolGroup>,
    /// Fallback config applied to new symbols when no explicit bot covers them.
    /// Set by the legacy `engine.configure` subject for backward-compat.
    global_config: Option<GlobalConfig>,
}

struct GlobalConfig {
    strategy_name: String,
    params_json: String,
}

impl Registry {
    pub fn new() -> Self {
        Self {
            groups: HashMap::new(),
            global_config: None,
        }
    }

    /// Register (or re-register) a bot on a symbol.
    pub fn register(
        &mut self,
        bot_id: String,
        symbol: String,
        strategy_name: String,
        params_json: String,
    ) -> Result<()> {
        let bot = BotHandle::new(bot_id, symbol.clone(), strategy_name, params_json)?;
        self.groups
            .entry(symbol.clone())
            .or_insert_with(|| SymbolGroup::new(symbol))
            .add(bot);
        Ok(())
    }

    /// Remove a bot by id. Cleans up empty groups.
    /// If bot_id is empty, removes ALL bots.
    pub fn deregister(&mut self, bot_id: &str) {
        if bot_id.is_empty() {
            self.groups.clear();
            return;
        }
        for group in self.groups.values_mut() {
            group.remove(bot_id);
        }
        self.groups.retain(|_, g| !g.is_empty());
    }

    /// Set global fallback config (legacy `engine.configure` support).
    /// Clears all existing bots (same semantics as before).
    pub fn set_global_config(&mut self, strategy_name: String, params_json: String) {
        self.groups.clear();
        self.global_config = Some(GlobalConfig {
            strategy_name,
            params_json,
        });
    }

    /// Process a bar. Auto-creates a fallback bot for new symbols if global
    /// config is set (backward-compat with `engine.configure` flow).
    pub fn on_bar(&mut self, bar: &Bar) -> Vec<(String, Vec<Signal>)> {
        // Auto-register fallback bot for unseen symbols
        if !self.groups.contains_key(&bar.symbol) {
            if let Some(gc) = &self.global_config {
                let bot_id = format!("global.{}", bar.symbol);
                let bot = BotHandle::new(
                    bot_id,
                    bar.symbol.clone(),
                    gc.strategy_name.clone(),
                    gc.params_json.clone(),
                );
                match bot {
                    Ok(b) => {
                        self.groups
                            .entry(bar.symbol.clone())
                            .or_insert_with(|| SymbolGroup::new(bar.symbol.clone()))
                            .add(b);
                    }
                    Err(e) => {
                        tracing::warn!(symbol = %bar.symbol, err = %e, "failed to build fallback strategy");
                        return vec![];
                    }
                }
            } else {
                return vec![];
            }
        }

        self.groups
            .get_mut(&bar.symbol)
            .map(|g| g.on_bar(bar))
            .unwrap_or_default()
    }

    /// Reset state for one symbol (or all if symbol is empty).
    /// Drops the SymbolGroup entirely (clears both bots and bar window).
    /// Fallback global bots will be lazily re-created on the next bar.
    pub fn reset(&mut self, symbol: &str) {
        if symbol.is_empty() {
            self.groups.clear();
        } else {
            self.groups.remove(symbol);
        }
    }

    pub fn list_bots(&self) -> Vec<(&str, &str, &str, &str)> {
        self.groups
            .values()
            .flat_map(|g| g.bot_infos())
            .map(|b| {
                (
                    b.bot_id.as_str(),
                    b.symbol.as_str(),
                    b.strategy_name.as_str(),
                    b.params_json.as_str(),
                )
            })
            .collect()
    }
}

impl Default for Registry {
    fn default() -> Self {
        let mut r = Self::new();
        // Same defaults as old EngineState::default()
        r.set_global_config(
            "ma_crossover".into(),
            r#"{"fast": 20, "slow": 50}"#.into(),
        );
        r
    }
}

// ── helpers ───────────────────────────────────────────────────────────────────

fn build_from_json(strategy_name: &str, params_json: &str) -> Result<Box<dyn Strategy>> {
    let value: serde_json::Value = if params_json.is_empty() {
        serde_json::json!({})
    } else {
        serde_json::from_str(params_json)
            .map_err(|e| anyhow!("invalid params_json: {e}"))?
    };

    // Convert JSON object to HashMap<String, f64> for factory
    let map: HashMap<String, f64> = match &value {
        serde_json::Value::Object(obj) => obj
            .iter()
            .filter_map(|(k, v)| v.as_f64().map(|f| (k.clone(), f)))
            .collect(),
        _ => HashMap::new(),
    };

    let params = params_from_map(&map);
    build_strategy(strategy_name, &params).map_err(|e| anyhow!("{e}"))
}

use serde::Deserialize;

use alm_core::Timeframe;

use super::timeframe::SUBSCRIBE_TFS;

// ── Per-symbol entry ──────────────────────────────────────────────────────────

/// A single entry in the `binance:` / `okx:` list inside `symbols.yaml`.
///
/// Two syntaxes are accepted (backward-compatible):
///
/// ```yaml
/// binance:
///   - BTCUSDT                            # shorthand — subscribes all SUBSCRIBE_TFS
///   - symbol: ETHUSDT
///     timeframes: [M1, H1, H4]           # explicit subset
/// ```
///
/// Unknown TF strings are dropped with a `warn!` at startup.
/// An empty `timeframes` list falls back to all [`SUBSCRIBE_TFS`].
#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum SymbolEntry {
    /// Plain string: `- BTCUSDT`
    Simple(String),
    /// Object: `- { symbol: BTCUSDT, timeframes: [M1, H4] }`
    Full {
        symbol: String,
        #[serde(default)]
        timeframes: Vec<String>,
    },
}

impl SymbolEntry {
    pub fn symbol(&self) -> &str {
        match self {
            Self::Simple(s) | Self::Full { symbol: s, .. } => s,
        }
    }

    /// Resolve the TF list for this entry.
    /// Falls back to all [`SUBSCRIBE_TFS`] when none are specified or the list is empty.
    pub fn timeframes(&self) -> Vec<Timeframe> {
        let raw: &[String] = match self {
            Self::Simple(_) => &[],
            Self::Full { timeframes, .. } => timeframes.as_slice(),
        };
        if raw.is_empty() {
            return SUBSCRIBE_TFS.to_vec();
        }
        raw.iter()
            .filter_map(|s| {
                let tf: Option<Timeframe> = s.to_ascii_uppercase().parse().ok();
                if tf.is_none() {
                    tracing::warn!(tf = %s, "symbols.yaml: unknown timeframe — skipping");
                }
                tf.filter(|t| SUBSCRIBE_TFS.contains(t))
            })
            .collect()
    }

    /// `(raw_symbol, Vec<Timeframe>)` — ready for feed spawn.
    pub fn to_symbol_tfs(&self) -> (String, Vec<Timeframe>) {
        (self.symbol().to_string(), self.timeframes())
    }
}

// ── Top-level config ──────────────────────────────────────────────────────────

/// Parsed content of `symbols.yaml` — the single source of truth for which symbols
/// and timeframes herald ingests live.
#[derive(Debug, Default, Deserialize)]
pub struct SymbolConfig {
    #[serde(default)]
    pub binance: Vec<SymbolEntry>,
    #[serde(default)]
    pub okx: Vec<SymbolEntry>,
}

impl SymbolConfig {
    /// Load from `HERALD_SYMBOLS_FILE` if set; returns `None` so the caller can
    /// fall back to the `HERALD_BINANCE_SYMBOLS` / `HERALD_OKX_SYMBOLS` env vars.
    pub fn from_env_file() -> anyhow::Result<Option<Self>> {
        let path = match std::env::var("HERALD_SYMBOLS_FILE") {
            Ok(p) => p,
            Err(_) => return Ok(None),
        };
        let text = std::fs::read_to_string(&path)
            .map_err(|e| anyhow::anyhow!("HERALD_SYMBOLS_FILE {path:?}: {e}"))?;
        let cfg: Self = serde_yaml::from_str(&text)
            .map_err(|e| anyhow::anyhow!("HERALD_SYMBOLS_FILE {path:?}: {e}"))?;
        Ok(Some(cfg))
    }

    /// Populate Binance entries from a plain string list (env-var fallback).
    /// Each symbol gets all [`SUBSCRIBE_TFS`] (same as the shorthand YAML form).
    pub fn set_binance_from_strings(&mut self, symbols: Vec<String>) {
        self.binance = symbols.into_iter().map(SymbolEntry::Simple).collect();
    }

    /// Populate OKX entries from a plain string list (env-var fallback).
    pub fn set_okx_from_strings(&mut self, symbols: Vec<String>) {
        self.okx = symbols.into_iter().map(SymbolEntry::Simple).collect();
    }

    // ── Feed spawn helpers ────────────────────────────────────────────────────

    /// `(raw_symbol, Vec<Timeframe>)` pairs for `feed::binance::spawn`.
    pub fn binance_symbol_tfs(&self) -> Vec<(String, Vec<Timeframe>)> {
        self.binance.iter().map(SymbolEntry::to_symbol_tfs).collect()
    }

    /// `(raw_symbol, Vec<Timeframe>)` pairs for `feed::okx::spawn`.
    pub fn okx_symbol_tfs(&self) -> Vec<(String, Vec<Timeframe>)> {
        self.okx.iter().map(SymbolEntry::to_symbol_tfs).collect()
    }

    // ── Utility ───────────────────────────────────────────────────────────────

    /// All symbols as exchange-prefixed ledger keys: `"binance:BTCUSDT"`, `"okx:BTC-USDT"`.
    pub fn all_prefixed(&self) -> Vec<String> {
        let mut all: Vec<String> = self
            .binance
            .iter()
            .map(|e| format!("binance:{}", e.symbol().to_uppercase()))
            .collect();
        for e in &self.okx {
            let key = format!("okx:{}", e.symbol());
            if !all.contains(&key) {
                all.push(key);
            }
        }
        all
    }

    /// All unique raw symbols across both exchanges.
    pub fn all_symbols(&self) -> Vec<String> {
        let mut all: Vec<String> =
            self.binance.iter().map(|e| e.symbol().to_string()).collect();
        for e in &self.okx {
            let s = e.symbol().to_string();
            if !all.contains(&s) {
                all.push(s);
            }
        }
        all
    }

    /// Split an exchange-prefixed key (`"binance:BTCUSDT"`) into `("binance", "BTCUSDT")`.
    pub fn split_prefix(key: &str) -> (&str, &str) {
        match key.find(':') {
            Some(i) => (&key[..i], &key[i + 1..]),
            None => ("", key),
        }
    }
}

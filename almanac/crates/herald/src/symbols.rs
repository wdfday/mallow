use serde::Deserialize;

#[derive(Debug, Default, Deserialize)]
pub struct SymbolConfig {
    #[serde(default)]
    pub binance: Vec<String>,
    #[serde(default)]
    pub okx: Vec<String>,
}

impl SymbolConfig {
    /// Load from `HERALD_SYMBOLS_FILE` if set, otherwise return empty config
    /// (caller falls back to `HERALD_BINANCE_SYMBOLS` / `HERALD_OKX_SYMBOLS` env vars).
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

    /// All symbols as exchange-prefixed keys: `"binance:BTCUSDT"`, `"okx:BTC-USDT"`, etc.
    /// This is the canonical form used in the ledger, registry, NATS subjects, and HTTP API.
    pub fn all_prefixed(&self) -> Vec<String> {
        let mut all: Vec<String> = self.binance.iter()
            .map(|s| format!("binance:{}", s.to_uppercase()))
            .collect();
        for s in &self.okx {
            let key = format!("okx:{s}");
            if !all.contains(&key) {
                all.push(key);
            }
        }
        all
    }

    /// All unique raw symbols across both exchanges (for HERALD_SYMBOLS warm-set override).
    pub fn all_symbols(&self) -> Vec<String> {
        let mut all = self.binance.clone();
        for s in &self.okx {
            if !all.contains(s) {
                all.push(s.clone());
            }
        }
        all
    }

    /// Parse exchange and raw symbol from a prefixed key like `"binance:BTCUSDT"`.
    /// Returns `(exchange, raw_symbol)`.
    pub fn split_prefix(key: &str) -> (&str, &str) {
        if let Some(pos) = key.find(':') {
            (&key[..pos], &key[pos + 1..])
        } else {
            ("", key)
        }
    }
}

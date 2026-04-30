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

    /// All unique symbols across both exchanges (for HERALD_SYMBOLS warm-set).
    pub fn all_symbols(&self) -> Vec<String> {
        let mut all = self.binance.clone();
        for s in &self.okx {
            if !all.contains(s) {
                all.push(s.clone());
            }
        }
        all
    }
}

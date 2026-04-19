//! `IndicatorSpec` — canonical description of an indicator instance,
//! used as a dedup key across the ledger.
//!
//! Two specs are equal iff they name the same indicator, on the same
//! source timeframe, with the same config. Equality is computed over a
//! canonical string form so that JSON key ordering doesn't matter.

use std::hash::{Hash, Hasher};

use alm_core::Timeframe;
use serde::{Deserialize, Serialize};
use serde_json::Value;

/// Canonical description of an indicator instance.
///
/// The `config` is the same JSON shape consumed by [`alm_indicator::IndicatorBox::from_config`],
/// e.g. `{ "type": "ema", "period": 20 }`. `source_tf` is `None` when the
/// indicator runs on the base timeframe of the owning `SymbolState`, and
/// `Some(tf)` when it needs a higher-timeframe resample.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IndicatorSpec {
    /// Short name derived from the config `"type"` field (`"ema"`, `"rsi"`, `"macd"`, ...).
    pub name: String,
    /// Full JSON config passed to `IndicatorBox::from_config`.
    pub config: Value,
    /// Optional source timeframe for MTF indicators. `None` = base TF.
    pub source_tf: Option<Timeframe>,
}

impl IndicatorSpec {
    /// Build from a JSON config — `name` is taken from the `"type"` field.
    pub fn from_config(config: Value, source_tf: Option<Timeframe>) -> anyhow::Result<Self> {
        let name = config
            .get("type")
            .and_then(Value::as_str)
            .ok_or_else(|| anyhow::anyhow!("IndicatorSpec: config missing 'type' field"))?
            .to_string();
        Ok(Self { name, config, source_tf })
    }

    /// Canonical string form for hashing / equality / display.
    ///
    /// Shape: `"ema|period=20|bar"` / `"macd|fast=12;signal=9;slow=26|bar"` /
    /// `"ema|period=200|H1"`. Keys are sorted alphabetically to make the
    /// representation independent of JSON ordering.
    pub fn canonical_key(&self) -> String {
        let tf_part = match self.source_tf {
            Some(tf) => tf.to_string(),
            None => "bar".to_string(),
        };
        let params_part = canonical_params(&self.config);
        format!("{}|{}|{}", self.name, params_part, tf_part)
    }

    /// Heuristic: the longest "period-like" number in the config.
    ///
    /// Used by the ledger to auto-expand `bar_window` capacity so that the
    /// indicator has enough history to warm up. Conservative — only scans
    /// keys that are known to represent lookback periods; ignores
    /// multipliers, offsets, noise thresholds, etc.
    pub fn longest_period(&self) -> usize {
        const PERIOD_KEYS: &[&str] = &[
            "period", "slow", "fast", "signal", "medium",
            "senkou_b", "kijun", "tenkan",
            "jaw", "teeth", "lips",
            "k_period", "d_period",
            "atr_period", "ema_period", "er_period",
            "first", "second",
            "rank_period", "rsi_period", "streak_period", "smooth_d",
            "lookback", "stop_period",
            "session_gap_mins",
        ];
        let obj = match self.config.as_object() {
            Some(o) => o,
            None => return 0,
        };
        let mut max = 0usize;
        for key in PERIOD_KEYS {
            if let Some(v) = obj.get(*key).and_then(Value::as_f64) {
                let v = v.round() as usize;
                if v > max { max = v; }
            }
        }
        max
    }

    /// True for indicators whose internal state resets on a session boundary
    /// (daily open). Currently only VWAP — scoped to avoid surprising users
    /// when the ledger skips a session.
    pub fn is_session_aware(&self) -> bool {
        matches!(self.name.as_str(), "vwap")
    }
}

impl PartialEq for IndicatorSpec {
    fn eq(&self, other: &Self) -> bool {
        self.canonical_key() == other.canonical_key()
    }
}
impl Eq for IndicatorSpec {}
impl Hash for IndicatorSpec {
    fn hash<H: Hasher>(&self, state: &mut H) {
        self.canonical_key().hash(state)
    }
}

impl std::fmt::Display for IndicatorSpec {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.canonical_key())
    }
}

fn canonical_params(config: &Value) -> String {
    let obj = match config.as_object() {
        Some(o) => o,
        None => return String::new(),
    };
    let mut parts: Vec<(String, String)> = obj
        .iter()
        .filter(|(k, _)| k.as_str() != "type")
        .map(|(k, v)| (k.clone(), format_value(v)))
        .collect();
    parts.sort_by(|a, b| a.0.cmp(&b.0));
    parts
        .into_iter()
        .map(|(k, v)| format!("{k}={v}"))
        .collect::<Vec<_>>()
        .join(";")
}

fn format_value(v: &Value) -> String {
    match v {
        Value::Number(n) => {
            if let Some(i) = n.as_i64() { return i.to_string(); }
            if let Some(u) = n.as_u64() { return u.to_string(); }
            if let Some(f) = n.as_f64() {
                if (f.fract() == 0.0) && f.is_finite() && f.abs() < 1e15 {
                    return format!("{}", f as i64);
                }
                return format!("{f}");
            }
            n.to_string()
        }
        Value::String(s) => s.clone(),
        Value::Bool(b) => b.to_string(),
        Value::Null => "null".into(),
        _ => v.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn equality_independent_of_key_order() {
        let a = IndicatorSpec::from_config(json!({"type":"macd","fast":12,"slow":26,"signal":9}), None).unwrap();
        let b = IndicatorSpec::from_config(json!({"type":"macd","signal":9,"slow":26,"fast":12}), None).unwrap();
        assert_eq!(a, b);
        assert_eq!(a.canonical_key(), b.canonical_key());
    }

    #[test]
    fn different_tf_is_not_equal() {
        let a = IndicatorSpec::from_config(json!({"type":"ema","period":200}), None).unwrap();
        let b = IndicatorSpec::from_config(json!({"type":"ema","period":200}), Some(Timeframe::H1)).unwrap();
        assert_ne!(a, b);
    }

    #[test]
    fn longest_period_picks_slow() {
        let s = IndicatorSpec::from_config(json!({"type":"macd","fast":12,"slow":26,"signal":9}), None).unwrap();
        assert_eq!(s.longest_period(), 26);
    }

    #[test]
    fn longest_period_ignores_multipliers() {
        let s = IndicatorSpec::from_config(json!({"type":"bbands","period":20,"multiplier":2.0}), None).unwrap();
        assert_eq!(s.longest_period(), 20);
    }

    #[test]
    fn vwap_is_session_aware() {
        let s = IndicatorSpec::from_config(json!({"type":"vwap"}), None).unwrap();
        assert!(s.is_session_aware());
        let e = IndicatorSpec::from_config(json!({"type":"ema","period":20}), None).unwrap();
        assert!(!e.is_session_aware());
    }
}

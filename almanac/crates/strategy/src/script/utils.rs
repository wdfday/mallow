use std::collections::HashMap;
use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use anyhow::Result;

pub(crate) const PERIOD_EXEMPT: &[&str] = &[
    // period arg maps to other parameters or is ignored entirely:
    "kalman", "gmma", "ao", "coppock", "bop", "obv", "vwap", "fractal",
    // handle period=0 with built-in defaults (documented in indicator_json_config):
    "pmo", "smi", "kst", "parabolic_sar", "uo",
];

pub(crate) fn parse_timeframe(s: &str) -> Option<Timeframe> {
    match s.to_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),
        "M3"  => Some(Timeframe::M3),
        "M5"  => Some(Timeframe::M5),
        "M10" => Some(Timeframe::M10),
        "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),
        "H2"  => Some(Timeframe::H2),
        "H4"  => Some(Timeframe::H4),
        "H6"  => Some(Timeframe::H6),
        "H12" => Some(Timeframe::H12),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _     => None,
    }
}

pub(crate) fn build_indicator_box(
    var_name: &str,
    ind_type: &str,
    period: usize,
    extra_params: &HashMap<String, f64>,
) -> Result<IndicatorBox> {
    if period == 0 && !PERIOD_EXEMPT.contains(&ind_type) {
        anyhow::bail!(
            "indicator '{}' (type '{}'): period must be ≥ 1, got 0",
            var_name, ind_type
        );
    }
    for (key, &val) in extra_params {
        if val < 0.0 {
            anyhow::bail!(
                "indicator '{}': parameter '{}' must be ≥ 0, got {}",
                var_name, key, val
            );
        }
    }
    let conf = crate::script::v1::indicator_json_config(ind_type, period, extra_params);
    IndicatorBox::from_config(&conf).map_err(|e| anyhow::anyhow!(e))
}

pub(crate) fn validate_candle_kind(kind: &str) -> Result<()> {
    match kind {
        "raw" | "heiken_ashi" | "ha" | "smooth_ha" | "smooth_heiken_ashi" => Ok(()),
        other => Err(anyhow::anyhow!(
            "unknown candle kind `{other}`; supported: \
             \"raw\", \"heiken_ashi\" (alias \"ha\"), \"smooth_ha\" (alias \"smooth_heiken_ashi\")"
        )),
    }
}

pub(crate) fn is_boolean_flag_field(field: &str) -> bool {
    alm_indicator::field_kind(field) == alm_indicator::FieldKind::Bool
}

pub(crate) fn scalar_out(scope: &rhai::Scope, name: &str) -> Option<f64> {
    scope
        .get_value::<f64>(name)
        .or_else(|| scope.get_value::<i64>(name).map(|v| v as f64))
}

pub(crate) fn bool_out(scope: &rhai::Scope, name: &str) -> bool {
    scope.get_value::<bool>(name).unwrap_or(false)
        || scope.get_value::<i64>(name).map(|v| v != 0).unwrap_or(false)
        || scope.get_value::<f64>(name).map(|v| v > 0.5).unwrap_or(false)
}

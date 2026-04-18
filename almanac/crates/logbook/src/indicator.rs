//! Indicator computation — feeds historical bars through one or more indicators
//! and returns the resulting time series.

use std::collections::HashMap;
use std::path::Path;

use anyhow::{Context, Result};
use alm_core::Bar;
use alm_data::BarFeed;
use alm_strategy::bar_resampler::TimeBarResampler;
use alm_strategy::dynamic::indicator_box::IndicatorBox;
use alm_strategy::expr::cel::parse_cel_indicator;
use serde_json::Value;

use crate::catalog::{auto_label, cel_to_config};
use crate::data::{find_parquet_files, load_bars, parse_date_ms};
use crate::types::{IndicatorConfig, IndicatorPoint, IndicatorRequest, IndicatorResponse};

/// Feed `bars` through the given indicator configs and return a label→series map.
/// Called by both `compute_indicators` and the unified data handler.
pub fn run_indicators_on_bars(
    bars: &[Bar],
    indicators: &[IndicatorConfig],
) -> Result<HashMap<String, Vec<IndicatorPoint>>> {
    struct BoundInd {
        label:     String,
        box_:      IndicatorBox,
        resampler: Option<TimeBarResampler>,
    }

    let mut inds: Vec<BoundInd> = indicators
        .iter()
        .map(|cfg| {
            if let Some(cel_expr) = &cfg.cel {
                let (base, args, interval_ms) = parse_cel_indicator(cel_expr)?;
                let n = args.first().copied().unwrap_or(0.0) as usize;
                let json_cfg = cel_to_config(&base, n);
                let box_ = IndicatorBox::from_config(&json_cfg)?;
                let resampler = interval_ms.map(TimeBarResampler::new);
                let label = cfg.label.clone().unwrap_or_else(|| {
                    let tf_prefix = interval_ms.map(|_| {
                        cel_expr.split('.').next()
                            .filter(|s| !s.is_empty())
                            .map(|s| format!("{}_", s))
                            .unwrap_or_default()
                    }).unwrap_or_default();
                    if args.is_empty() {
                        format!("{tf_prefix}{base}")
                    } else {
                        let arg_str: Vec<String> = args.iter().map(|a| format!("{}", *a as i64)).collect();
                        format!("{tf_prefix}{}_{}", base, arg_str.join("_"))
                    }
                });
                Ok(BoundInd { label, box_, resampler })
            } else {
                let label = cfg.label.clone()
                    .unwrap_or_else(|| auto_label(&cfg.config));
                let box_ = IndicatorBox::from_config(&Value::Object(cfg.config.clone()))?;
                Ok(BoundInd { label, box_, resampler: None })
            }
        })
        .collect::<Result<Vec<_>>>()?;

    let mut series: HashMap<String, Vec<IndicatorPoint>> =
        inds.iter().map(|b| (b.label.clone(), Vec::new())).collect();

    for bar in bars {
        for b in &mut inds {
            let agg = match &mut b.resampler {
                Some(rs) => rs.push(bar),
                None     => Some(bar.clone()),
            };
            if let Some(htf_bar) = agg {
                if let Some(fields) = b.box_.update(&htf_bar) {
                    series.get_mut(&b.label).unwrap().push(IndicatorPoint {
                        t: htf_bar.timestamp,
                        fields,
                    });
                }
            }
        }
    }

    Ok(series)
}

/// Compute one or more indicators over historical data (legacy endpoint).
pub fn compute_indicators(req: IndicatorRequest, data_dir: &Path) -> Result<IndicatorResponse> {
    let symbol = if req.symbol.is_empty() { "BTCUSD".to_string() } else { req.symbol };

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms   = req.to.as_deref().and_then(|s| {
        parse_date_ms(s).map(|ms| ms + 86_400_000 - 1)
    });
    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let exchange          = req.exchange.as_deref().unwrap_or("us");

    let files = find_parquet_files(data_dir, &symbol, req.timeframe.as_deref());
    let mut feed = load_bars(&files, &symbol, from_ms, to_ms, market_hours_only, exchange)
        .with_context(|| format!("loading data for '{}'", symbol))?;

    let bars_total = feed.len();
    let mut bars = Vec::with_capacity(bars_total);
    while let Some(bar) = feed.next() {
        bars.push(bar);
    }

    let series = run_indicators_on_bars(&bars, &req.indicators)?;
    Ok(IndicatorResponse { symbol, bars: bars_total, series })
}

//! Candlestick chart with indicator overlays.
//!
//! Two rendering backends:
//! - `to_html()` — interactive ECharts HTML (charming), returns `String`
//! - `to_png(path)` / `to_svg(path)` — static image file (plotters)
//!
//! # Example
//! ```no_run
//! use alm_chart::candle::CandleChart;
//! use alm_core::Bar;
//!
//! let bars: Vec<Bar> = vec![/* ... */];
//! CandleChart::new(&bars)
//!     .with_title("BTCUSDT 1h")
//!     .indicator("SMA20", vec![None, None, Some(42000.0)])
//!     .volume(true)
//!     .to_html()
//!     .unwrap();
//! ```

use anyhow::Result;
use alm_core::{Bar, Trade};
use charming::{
    component::{Axis, DataZoom, DataZoomType, Grid, Legend, Title},
    datatype::CompositeValue,
    element::AxisType,
    series::{Bar as BarSeries, Candlestick, Line},
    Chart, HtmlRenderer,
};
use chrono::{TimeZone, Utc};
use plotters::prelude::*;
use std::path::Path;

/// A named indicator line aligned 1:1 with the bar slice.
pub struct IndicatorLine {
    pub name: String,
    /// `None` for warmup bars where the indicator has no value yet.
    pub values: Vec<Option<f64>>,
}

/// Candlestick chart builder.
pub struct CandleChart<'a> {
    bars: &'a [Bar],
    title: Option<String>,
    indicators: Vec<IndicatorLine>,
    overlay: Option<crate::pattern::PatternOverlay>,
    trades: Vec<&'a Trade>,
    show_volume: bool,
}

impl<'a> CandleChart<'a> {
    pub fn new(bars: &'a [Bar]) -> Self {
        Self {
            bars,
            title: None,
            indicators: Vec::new(),
            overlay: None,
            trades: Vec::new(),
            show_volume: false,
        }
    }

    pub fn with_title(mut self, title: impl Into<String>) -> Self {
        self.title = Some(title.into());
        self
    }

    /// Overlay an indicator line. `values` must align 1:1 with `bars`.
    pub fn indicator(mut self, name: impl Into<String>, values: Vec<Option<f64>>) -> Self {
        self.indicators.push(IndicatorLine {
            name: name.into(),
            values,
        });
        self
    }

    /// Overlay pattern annotations (pivots, trendlines, zones).
    pub fn overlay(mut self, o: crate::pattern::PatternOverlay) -> Self {
        self.overlay = Some(o);
        self
    }

    /// Mark trade entry/exit points. `bar_lookup` maps a Unix-ms timestamp to
    /// its 0-based index in the `bars` slice (use `bars.iter().position(...)`)
    pub fn trades(mut self, trades: &'a [Trade]) -> Self {
        self.trades = trades.iter().collect();
        self
    }

    /// Show volume subplot below the price pane.
    pub fn volume(mut self, show: bool) -> Self {
        self.show_volume = show;
        self
    }

    // ── charming / ECharts ───────────────────────────────────────────────────

    /// Render to a self-contained interactive HTML string.
    pub fn to_html(&self) -> Result<String> {
        let categories: Vec<String> = self
            .bars
            .iter()
            .map(|b| {
                Utc.timestamp_millis_opt(b.timestamp)
                    .single()
                    .map(|dt| dt.format("%Y-%m-%d %H:%M").to_string())
                    .unwrap_or_else(|| b.timestamp.to_string())
            })
            .collect();

        // ECharts candlestick expects [open, close, low, high]
        let ohlc: Vec<Vec<CompositeValue>> = self
            .bars
            .iter()
            .map(|b| {
                vec![
                    CompositeValue::from(b.open),
                    CompositeValue::from(b.close),
                    CompositeValue::from(b.low),
                    CompositeValue::from(b.high),
                ]
            })
            .collect();

        // Separate oscillator-like indicators (RSI, MACD-histogram, etc.)
        // from price overlays (SMA, EMA, BB).  Heuristic: if max value ≤ 110
        // and all Some values are in [−200, 200] treat it as an oscillator.
        let (price_inds, osc_inds): (Vec<_>, Vec<_>) = self
            .indicators
            .iter()
            .partition(|ind| {
                let vals: Vec<f64> = ind.values.iter().filter_map(|v| *v).collect();
                if vals.is_empty() {
                    return true; // price overlay by default
                }
                let max = vals.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
                let min = vals.iter().cloned().fold(f64::INFINITY, f64::min);
                // oscillators: small bounded range (e.g. RSI 0-100, stoch 0-100)
                !(max <= 110.0 && min >= -10.0 && (max - min) < 110.0)
            });

        let has_osc = !osc_inds.is_empty();
        let has_vol = self.show_volume;

        // Grid layout: price | [osc] | [volume]
        let (price_bottom, osc_top, osc_bottom, vol_top) = if has_osc && has_vol {
            ("50%", "52%", "27%", "75%")
        } else if has_osc {
            ("40%", "42%", "5%", "")
        } else if has_vol {
            ("28%", "", "", "75%")
        } else {
            ("8%", "", "", "")
        };

        // Axis indices:
        //   0 → price x/y
        //   1 → osc x/y  (if has_osc)
        //   2 → vol x/y  (if has_vol, or 1 when no osc)
        let vol_axis_idx: u32 = if has_osc { 2 } else { 1 };

        let mut chart = Chart::new()
            // Grid 0 — price pane
            .grid(Grid::new().bottom(price_bottom))
            .x_axis(
                Axis::new()
                    .type_(AxisType::Category)
                    .grid_index(0)
                    .data(categories.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
            )
            .y_axis(Axis::new().type_(AxisType::Value).grid_index(0).scale(true))
            .data_zoom(
                DataZoom::new()
                    .type_(DataZoomType::Inside)
                    .x_axis_index(vec![0, 1, 2]),
            )
            .data_zoom(
                DataZoom::new()
                    .type_(DataZoomType::Slider)
                    .x_axis_index(vec![0, 1, 2])
                    .bottom("1%"),
            )
            .series(Candlestick::new().data(ohlc));

        if let Some(ref t) = self.title {
            chart = chart.title(Title::new().text(t.as_str()));
        }

        let mut legend_data: Vec<String> = Vec::new();

        // ── Price-overlay indicators (SMA, EMA, BB…) on grid 0 ───────────────
        for ind in &price_inds {
            legend_data.push(ind.name.clone());
            let data: Vec<CompositeValue> = ind
                .values
                .iter()
                .map(|v| match v {
                    Some(f) => CompositeValue::from(*f),
                    None => CompositeValue::from("-"),
                })
                .collect();
            chart = chart.series(
                Line::new()
                    .name(ind.name.as_str())
                    .x_axis_index(0)
                    .y_axis_index(0)
                    .data(data),
            );
        }

        // ── Oscillator pane (grid 1) — RSI, Stoch, etc. ───────────────────────
        if has_osc {
            chart = chart
                .grid(Grid::new().top(osc_top).bottom(osc_bottom))
                .x_axis(
                    Axis::new()
                        .type_(AxisType::Category)
                        .grid_index(1)
                        .data(categories.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
                )
                .y_axis(Axis::new().type_(AxisType::Value).grid_index(1).scale(false));

            for ind in &osc_inds {
                legend_data.push(ind.name.clone());
                let data: Vec<CompositeValue> = ind
                    .values
                    .iter()
                    .map(|v| match v {
                        Some(f) => CompositeValue::from(*f),
                        None => CompositeValue::from("-"),
                    })
                    .collect();
                chart = chart.series(
                    Line::new()
                        .name(ind.name.as_str())
                        .x_axis_index(1)
                        .y_axis_index(1)
                        .data(data),
                );
            }
        }

        // ── Trade entry/exit markers (scatter on price pane) ─────────────────
        if !self.trades.is_empty() {
            use charming::series::Scatter;
            let entries: Vec<CompositeValue> = self
                .trades
                .iter()
                .filter_map(|t| {
                    let idx = self
                        .bars
                        .iter()
                        .position(|b| b.timestamp == t.entry_timestamp)?;
                    Some(CompositeValue::from(vec![
                        CompositeValue::from(idx as f64),
                        CompositeValue::from(t.entry_price),
                    ]))
                })
                .collect();
            let exits: Vec<CompositeValue> = self
                .trades
                .iter()
                .filter_map(|t| {
                    let idx = self
                        .bars
                        .iter()
                        .position(|b| b.timestamp == t.exit_timestamp)?;
                    Some(CompositeValue::from(vec![
                        CompositeValue::from(idx as f64),
                        CompositeValue::from(t.exit_price),
                    ]))
                })
                .collect();

            if !entries.is_empty() {
                chart = chart.series(
                    Scatter::new()
                        .name("Entry")
                        .x_axis_index(0)
                        .y_axis_index(0)
                        .data(entries),
                );
                legend_data.push("Entry".into());
            }
            if !exits.is_empty() {
                chart = chart.series(
                    Scatter::new()
                        .name("Exit")
                        .x_axis_index(0)
                        .y_axis_index(0)
                        .data(exits),
                );
                legend_data.push("Exit".into());
            }
        }

        // ── Pattern overlay (pivots + trendlines on price pane) ──────────────
        if let Some(ref ov) = self.overlay {
            if !ov.pivots.is_empty() {
                use charming::series::Scatter;
                chart = chart.series(
                    Scatter::new()
                        .name("Pivots")
                        .x_axis_index(0)
                        .y_axis_index(0)
                        .data(
                            ov.pivots
                                .iter()
                                .map(|p| {
                                    CompositeValue::from(vec![
                                        CompositeValue::from(p.bar_index as f64),
                                        CompositeValue::from(p.price),
                                    ])
                                })
                                .collect::<Vec<_>>(),
                        ),
                );
                legend_data.push("Pivots".into());
            }
            for tl in &ov.trendlines {
                let label = tl.label.as_deref().unwrap_or("Trendline").to_string();
                let data = vec![
                    CompositeValue::from(vec![
                        CompositeValue::from(tl.from.0 as f64),
                        CompositeValue::from(tl.from.1),
                    ]),
                    CompositeValue::from(vec![
                        CompositeValue::from(tl.to.0 as f64),
                        CompositeValue::from(tl.to.1),
                    ]),
                ];
                chart = chart.series(
                    Line::new()
                        .name(label.as_str())
                        .x_axis_index(0)
                        .y_axis_index(0)
                        .data(data),
                );
                legend_data.push(label);
            }
        }

        if !legend_data.is_empty() {
            chart = chart.legend(
                Legend::new()
                    .top("5%")
                    .data(legend_data.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
            );
        }

        // ── Volume subplot ────────────────────────────────────────────────────
        if has_vol {
            let volumes: Vec<CompositeValue> = self
                .bars
                .iter()
                .map(|b| {
                    // Color: green if bullish, red if bearish
                    CompositeValue::from(b.volume)
                })
                .collect();
            let vol_grid_idx = vol_axis_idx;
            chart = chart
                .grid(Grid::new().top(vol_top).bottom("2%"))
                .x_axis(
                    Axis::new()
                        .type_(AxisType::Category)
                        .grid_index(vol_grid_idx)
                        .data(categories.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
                )
                .y_axis(Axis::new().type_(AxisType::Value).grid_index(vol_grid_idx))
                .series(
                    BarSeries::new()
                        .name("Volume")
                        .x_axis_index(vol_axis_idx)
                        .y_axis_index(vol_axis_idx)
                        .data(volumes),
                );
        }

        let renderer = HtmlRenderer::new(
            self.title.as_deref().unwrap_or("Chart"),
            1200,
            600,
        );
        renderer.render(&chart).map_err(|e| anyhow::anyhow!("{e}"))
    }

    // ── plotters / static output ─────────────────────────────────────────────

    /// Render to a PNG file. Useful for test artifacts and CI reports.
    pub fn to_png(&self, path: impl AsRef<Path>) -> Result<()> {
        let path = path.as_ref();
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let root = BitMapBackend::new(path, (1200, 600)).into_drawing_area();
        self.draw_plotters(&root)?;
        root.present()?;
        Ok(())
    }

    /// Render to an SVG file.
    pub fn to_svg(&self, path: impl AsRef<Path>) -> Result<()> {
        let path = path.as_ref();
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let root = SVGBackend::new(path, (1200, 600)).into_drawing_area();
        self.draw_plotters(&root)?;
        root.present()?;
        Ok(())
    }

    fn draw_plotters<DB: DrawingBackend>(
        &self,
        root: &DrawingArea<DB, plotters::coord::Shift>,
    ) -> Result<()>
    where
        DB::ErrorType: 'static,
    {
        root.fill(&WHITE)
            .map_err(|e| anyhow::anyhow!("fill: {e}"))?;

        let (min_low, max_high) = self
            .bars
            .iter()
            .fold((f64::MAX, f64::MIN), |(lo, hi), b| {
                (lo.min(b.low), hi.max(b.high))
            });
        let pad = (max_high - min_low) * 0.05;

        let mut chart = ChartBuilder::on(root)
            .caption(
                self.title.as_deref().unwrap_or(""),
                ("sans-serif", 18).into_font(),
            )
            .margin(20u32)
            .x_label_area_size(30u32)
            .y_label_area_size(60u32)
            .build_cartesian_2d(0usize..self.bars.len(), (min_low - pad)..(max_high + pad))
            .map_err(|e| anyhow::anyhow!("build chart: {e}"))?;

        chart
            .configure_mesh()
            .draw()
            .map_err(|e| anyhow::anyhow!("mesh: {e}"))?;

        // Candlesticks
        chart
            .draw_series(self.bars.iter().enumerate().map(|(i, b)| {
                let style = if b.close >= b.open {
                    ShapeStyle::from(&GREEN).filled()
                } else {
                    ShapeStyle::from(&RED).filled()
                };
                CandleStick::new(i, b.open, b.high, b.low, b.close, style, style, 5)
            }))
            .map_err(|e| anyhow::anyhow!("candles: {e}"))?;

        // Indicator overlays
        let colors = [&BLUE, &MAGENTA, &CYAN, &BLACK];
        for (idx, ind) in self.indicators.iter().enumerate() {
            let color = colors[idx % colors.len()];
            let points: Vec<(usize, f64)> = ind
                .values
                .iter()
                .enumerate()
                .filter_map(|(i, v)| v.map(|f| (i, f)))
                .collect();
            chart
                .draw_series(LineSeries::new(points, color))
                .map_err(|e| anyhow::anyhow!("indicator {}: {e}", ind.name))?
                .label(ind.name.as_str())
                .legend(move |(x, y)| PathElement::new(vec![(x, y), (x + 18, y)], color));
        }

        // Trade entry/exit markers
        for trade in &self.trades {
            let entry_idx = self
                .bars
                .iter()
                .position(|b| b.timestamp == trade.entry_timestamp);
            let exit_idx = self
                .bars
                .iter()
                .position(|b| b.timestamp == trade.exit_timestamp);
            if let Some(i) = entry_idx {
                chart
                    .draw_series(std::iter::once(Circle::new(
                        (i, trade.entry_price),
                        6i32,
                        ShapeStyle::from(&GREEN).filled(),
                    )))
                    .map_err(|e| anyhow::anyhow!("trade entry: {e}"))?;
            }
            if let Some(i) = exit_idx {
                let color = if trade.pnl >= 0.0 { &BLUE } else { &RED };
                chart
                    .draw_series(std::iter::once(Circle::new(
                        (i, trade.exit_price),
                        6i32,
                        ShapeStyle::from(color).filled(),
                    )))
                    .map_err(|e| anyhow::anyhow!("trade exit: {e}"))?;
            }
        }

        // Pattern overlay
        if let Some(ref ov) = self.overlay {
            ov.draw_plotters(&mut chart)?;
        }

        let has_legend = !self.indicators.is_empty() || !self.trades.is_empty();
        if has_legend {
            chart
                .configure_series_labels()
                .background_style(WHITE.mix(0.8))
                .draw()
                .map_err(|e| anyhow::anyhow!("legend: {e}"))?;
        }

        Ok(())
    }
}

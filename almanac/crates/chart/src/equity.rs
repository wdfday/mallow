//! Equity curve and drawdown chart from a `Portfolio`.
//!
//! # Example
//! ```no_run
//! use alm_chart::equity::EquityChart;
//! use alm_core::portfolio::Portfolio;
//!
//! let portfolio = Portfolio::new(10_000.0);
//! // ... run backtest ...
//! let html = EquityChart::new(&portfolio).with_title("Strategy A").to_html().unwrap();
//! ```

use anyhow::Result;
use alm_core::portfolio::Portfolio;
use charming::{
    component::{Axis, Grid, Legend, Title},
    datatype::CompositeValue,
    element::AxisType,
    series::Line,
    Chart, HtmlRenderer,
};
use chrono::{TimeZone, Utc};
use plotters::prelude::*;
use std::path::Path;

pub struct EquityChart<'a> {
    portfolio: &'a Portfolio,
    title: Option<String>,
}

impl<'a> EquityChart<'a> {
    pub fn new(portfolio: &'a Portfolio) -> Self {
        Self {
            portfolio,
            title: None,
        }
    }

    pub fn with_title(mut self, title: impl Into<String>) -> Self {
        self.title = Some(title.into());
        self
    }

    // ── charming ─────────────────────────────────────────────────────────────

    /// Interactive HTML: equity curve (top) + drawdown (bottom).
    pub fn to_html(&self) -> Result<String> {
        let curve = &self.portfolio.equity_curve;
        if curve.is_empty() {
            anyhow::bail!("equity curve is empty");
        }

        let categories: Vec<String> = curve
            .iter()
            .map(|p| {
                Utc.timestamp_millis_opt(p.timestamp)
                    .single()
                    .map(|dt| dt.format("%Y-%m-%d %H:%M").to_string())
                    .unwrap_or_else(|| p.timestamp.to_string())
            })
            .collect();

        let equity_vals: Vec<CompositeValue> = curve
            .iter()
            .map(|p| CompositeValue::from((p.equity * 100.0).round() / 100.0))
            .collect();

        let drawdown_vals: Vec<CompositeValue> = drawdown_series(curve)
            .iter()
            .map(|&d| CompositeValue::from((d * 10000.0).round() / 10000.0))
            .collect();

        let chart = Chart::new()
            .title(Title::new().text(self.title.as_deref().unwrap_or("Equity Curve")))
            .legend(Legend::new().data(vec!["Equity", "Drawdown %"]))
            // Top grid — equity
            .grid(Grid::new().bottom("38%"))
            .x_axis(
                Axis::new()
                    .type_(AxisType::Category)
                    .grid_index(0)
                    .data(categories.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
            )
            .y_axis(
                Axis::new()
                    .type_(AxisType::Value)
                    .grid_index(0)
                    .scale(true),
            )
            .series(
                Line::new()
                    .name("Equity")
                    .x_axis_index(0)
                    .y_axis_index(0)
                    .data(equity_vals),
            )
            // Bottom grid — drawdown
            .grid(Grid::new().top("65%").bottom("5%"))
            .x_axis(
                Axis::new()
                    .type_(AxisType::Category)
                    .grid_index(1)
                    .data(categories.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
            )
            .y_axis(Axis::new().type_(AxisType::Value).grid_index(1))
            .series(
                Line::new()
                    .name("Drawdown %")
                    .x_axis_index(1)
                    .y_axis_index(1)
                    .data(drawdown_vals),
            );

        let renderer = HtmlRenderer::new(
            self.title.as_deref().unwrap_or("Equity"),
            1200,
            600,
        );
        renderer.render(&chart).map_err(|e| anyhow::anyhow!("{e}"))
    }

    // ── plotters ──────────────────────────────────────────────────────────────

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
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        let (upper, lower) = root.split_vertically(400u32);
        let curve = &self.portfolio.equity_curve;
        if curve.is_empty() {
            return Ok(());
        }

        let min_eq = curve.iter().map(|p| p.equity).fold(f64::MAX, f64::min);
        let max_eq = curve.iter().map(|p| p.equity).fold(f64::MIN, f64::max);
        let pad = (max_eq - min_eq) * 0.05;

        // Equity curve
        let mut eq_chart = ChartBuilder::on(&upper)
            .caption(
                self.title.as_deref().unwrap_or("Equity Curve"),
                ("sans-serif", 16).into_font(),
            )
            .margin(10u32)
            .x_label_area_size(0u32)
            .y_label_area_size(60u32)
            .build_cartesian_2d(0usize..curve.len(), (min_eq - pad)..(max_eq + pad))
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        eq_chart
            .configure_mesh()
            .draw()
            .map_err(|e| anyhow::anyhow!("{e}"))?;
        eq_chart
            .draw_series(LineSeries::new(
                curve.iter().enumerate().map(|(i, p)| (i, p.equity)),
                &BLUE,
            ))
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        // Drawdown
        let dd = drawdown_series(curve);
        let min_dd = dd.iter().cloned().fold(0.0f64, f64::min);

        let mut dd_chart = ChartBuilder::on(&lower)
            .caption("Drawdown %", ("sans-serif", 14).into_font())
            .margin(10u32)
            .x_label_area_size(30u32)
            .y_label_area_size(60u32)
            .build_cartesian_2d(0usize..curve.len(), (min_dd * 100.0 * 1.1)..0.1f64)
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        dd_chart
            .configure_mesh()
            .draw()
            .map_err(|e| anyhow::anyhow!("{e}"))?;
        dd_chart
            .draw_series(AreaSeries::new(
                dd.iter().enumerate().map(|(i, &d)| (i, d * 100.0)),
                0.0,
                RED.mix(0.3),
            ))
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        Ok(())
    }
}

/// Compute per-bar drawdown as fraction from running peak.
pub(crate) fn drawdown_series(curve: &[alm_core::portfolio::EquityPoint]) -> Vec<f64> {
    let mut peak = f64::MIN;
    curve
        .iter()
        .map(|p| {
            peak = peak.max(p.equity);
            if peak > 0.0 {
                (p.equity - peak) / peak
            } else {
                0.0
            }
        })
        .collect()
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::portfolio::EquityPoint;

    fn pt(t: i64, eq: f64) -> EquityPoint {
        EquityPoint { timestamp: t, equity: eq }
    }

    #[test]
    fn flat_equity_zero_drawdown() {
        let curve = vec![pt(0, 1000.0), pt(1, 1000.0), pt(2, 1000.0)];
        let dd = drawdown_series(&curve);
        assert!(dd.iter().all(|&d| d == 0.0));
    }

    #[test]
    fn monotone_rising_zero_drawdown() {
        let curve = vec![pt(0, 1000.0), pt(1, 1100.0), pt(2, 1200.0)];
        let dd = drawdown_series(&curve);
        assert!(dd.iter().all(|&d| d == 0.0), "no drawdown in rising equity");
    }

    #[test]
    fn drawdown_after_peak() {
        // Peak at 1200, then drops to 1100 → dd = (1100-1200)/1200 = -1/12
        let curve = vec![pt(0, 1000.0), pt(1, 1200.0), pt(2, 1100.0)];
        let dd = drawdown_series(&curve);
        assert_eq!(dd[0], 0.0); // at peak (first bar)
        assert_eq!(dd[1], 0.0); // new peak
        let expected = (1100.0 - 1200.0) / 1200.0;
        assert!((dd[2] - expected).abs() < 1e-10, "dd[2]={} expected={}", dd[2], expected);
    }

    #[test]
    fn running_peak_tracks_correctly() {
        // 1000 → 1500 (peak) → 1200 → 1600 (new peak) → 1400
        let curve = vec![
            pt(0, 1000.0),
            pt(1, 1500.0),
            pt(2, 1200.0),
            pt(3, 1600.0),
            pt(4, 1400.0),
        ];
        let dd = drawdown_series(&curve);
        assert_eq!(dd[1], 0.0); // peak
        assert!((dd[2] - (1200.0 - 1500.0) / 1500.0).abs() < 1e-10);
        assert_eq!(dd[3], 0.0); // new peak
        assert!((dd[4] - (1400.0 - 1600.0) / 1600.0).abs() < 1e-10);
    }

    #[test]
    fn empty_curve_returns_empty() {
        let dd = drawdown_series(&[]);
        assert!(dd.is_empty());
    }
}

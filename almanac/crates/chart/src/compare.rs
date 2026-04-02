//! Side-by-side comparison chart for cross-validating indicators against a reference.
//!
//! The primary use case is comparing your own indicator output against a
//! ground-truth series (e.g. TA-Lib exported from Python) to catch off-by-one
//! errors, warmup differences, or precision issues.
//!
//! # Example — RSI cross-validate
//! ```no_run
//! use alm_chart::compare::CompareChart;
//!
//! let my_rsi: Vec<Option<f64>>     = vec![/* alm-indicator output */];
//! let talib_rsi: Vec<Option<f64>>  = vec![/* loaded from CSV */];
//!
//! CompareChart::new("RSI-14 cross-validate")
//!     .series("alm-indicator", my_rsi)
//!     .series("TA-Lib", talib_rsi)
//!     .to_html()
//!     .unwrap();
//! ```
//!
//! # Example — indicator on price chart vs reference
//! ```no_run
//! use alm_chart::compare::OverlayCompare;
//! use alm_core::Bar;
//!
//! let bars: Vec<Bar> = vec![/* ... */];
//! OverlayCompare::new(&bars, "SMA20 compare")
//!     .left("alm-indicator SMA20", vec![/* my values */])
//!     .right("TA-Lib SMA20",      vec![/* ref values */])
//!     .to_html()
//!     .unwrap();
//! ```

use anyhow::Result;
use charming::{
    component::{Axis, Legend, Title},
    datatype::CompositeValue,
    element::AxisType,
    series::Line,
    Chart, HtmlRenderer,
};
use plotters::prelude::*;
use std::path::Path;

// ── CompareChart ──────────────────────────────────────────────────────────────

/// Plot multiple named series on a single chart for direct comparison.
/// Useful for indicator value comparison where price context is not needed.
pub struct CompareChart {
    title: String,
    series: Vec<(String, Vec<Option<f64>>)>,
}

impl CompareChart {
    pub fn new(title: impl Into<String>) -> Self {
        Self {
            title: title.into(),
            series: Vec::new(),
        }
    }

    pub fn series(mut self, name: impl Into<String>, values: Vec<Option<f64>>) -> Self {
        self.series.push((name.into(), values));
        self
    }

    /// Render to interactive HTML.
    pub fn to_html(&self) -> Result<String> {
        let max_len = self.series.iter().map(|(_, v)| v.len()).max().unwrap_or(0);
        let categories: Vec<String> = (0..max_len).map(|i| i.to_string()).collect();

        let mut chart = Chart::new()
            .title(Title::new().text(self.title.as_str()))
            .x_axis(
                Axis::new()
                    .type_(AxisType::Category)
                    .data(categories.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
            )
            .y_axis(Axis::new().type_(AxisType::Value).scale(true));

        let mut legend_data: Vec<String> = Vec::new();
        for (name, vals) in &self.series {
            legend_data.push(name.clone());
            let data: Vec<CompositeValue> = vals
                .iter()
                .map(|v| match v {
                    Some(f) => CompositeValue::from(*f),
                    None => CompositeValue::from("-"),
                })
                .collect();
            chart = chart.series(Line::new().name(name.as_str()).data(data));
        }

        chart = chart.legend(
            Legend::new()
                .data(legend_data.iter().map(|s| s.as_str()).collect::<Vec<_>>()),
        );

        let renderer = HtmlRenderer::new(self.title.as_str(), 1200, 400);
        renderer.render(&chart).map_err(|e| anyhow::anyhow!("{e}"))
    }

    /// Render to PNG file.
    pub fn to_png(&self, path: impl AsRef<Path>) -> Result<()> {
        let path = path.as_ref();
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let root = BitMapBackend::new(path, (1200, 400)).into_drawing_area();
        self.draw_plotters(&root)?;
        root.present()?;
        Ok(())
    }

    pub fn to_svg(&self, path: impl AsRef<Path>) -> Result<()> {
        let path = path.as_ref();
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let root = SVGBackend::new(path, (1200, 400)).into_drawing_area();
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
        root.fill(&WHITE).map_err(|e| anyhow::anyhow!("{e}"))?;

        let max_len = self.series.iter().map(|(_, v)| v.len()).max().unwrap_or(0);

        let (min_v, max_v) = self
            .series
            .iter()
            .flat_map(|(_, v)| v.iter().flatten())
            .fold((f64::MAX, f64::MIN), |(lo, hi), &v| {
                (lo.min(v), hi.max(v))
            });
        let pad = ((max_v - min_v) * 0.05).max(0.01);

        let mut chart = ChartBuilder::on(root)
            .caption(self.title.as_str(), ("sans-serif", 16).into_font())
            .margin(20u32)
            .x_label_area_size(30u32)
            .y_label_area_size(60u32)
            .build_cartesian_2d(0usize..max_len, (min_v - pad)..(max_v + pad))
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        chart
            .configure_mesh()
            .draw()
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        let colors = [
            &BLUE, &RED, &GREEN, &MAGENTA, &CYAN, &BLACK,
        ];

        for (idx, (name, vals)) in self.series.iter().enumerate() {
            let color = colors[idx % colors.len()];
            let points: Vec<(usize, f64)> = vals
                .iter()
                .enumerate()
                .filter_map(|(i, v)| v.map(|f| (i, f)))
                .collect();
            chart
                .draw_series(LineSeries::new(points, color))
                .map_err(|e| anyhow::anyhow!("{name}: {e}"))?
                .label(name.as_str())
                .legend(move |(x, y)| PathElement::new(vec![(x, y), (x + 18, y)], color));
        }

        chart
            .configure_series_labels()
            .background_style(WHITE.mix(0.8))
            .draw()
            .map_err(|e| anyhow::anyhow!("{e}"))?;

        Ok(())
    }
}

// ── OverlayCompare ────────────────────────────────────────────────────────────

/// Two candlestick charts side by side, each with its own indicator overlay.
/// Ideal for comparing "my algo output on price" vs "TA-Lib output on price".
pub struct OverlayCompare<'a> {
    bars: &'a [alm_core::Bar],
    title: String,
    left: Option<(String, Vec<Option<f64>>)>,
    right: Option<(String, Vec<Option<f64>>)>,
}

impl<'a> OverlayCompare<'a> {
    pub fn new(bars: &'a [alm_core::Bar], title: impl Into<String>) -> Self {
        Self {
            bars,
            title: title.into(),
            left: None,
            right: None,
        }
    }

    pub fn left(mut self, name: impl Into<String>, values: Vec<Option<f64>>) -> Self {
        self.left = Some((name.into(), values));
        self
    }

    pub fn right(mut self, name: impl Into<String>, values: Vec<Option<f64>>) -> Self {
        self.right = Some((name.into(), values));
        self
    }

    /// Render side-by-side HTML using `<iframe srcdoc>` so each chart is an
    /// isolated HTML document (avoids nested `<html>` tag issues).
    pub fn to_html(&self) -> Result<String> {
        use crate::candle::CandleChart;

        let left_html = {
            let mut c = CandleChart::new(self.bars).with_title(format!(
                "{} — {}",
                self.title,
                self.left
                    .as_ref()
                    .map(|(n, _)| n.as_str())
                    .unwrap_or("left")
            ));
            if let Some((name, vals)) = &self.left {
                c = c.indicator(name.clone(), vals.clone());
            }
            c.to_html()?
        };

        let right_html = {
            let mut c = CandleChart::new(self.bars).with_title(format!(
                "{} — {}",
                self.title,
                self.right
                    .as_ref()
                    .map(|(n, _)| n.as_str())
                    .unwrap_or("right")
            ));
            if let Some((name, vals)) = &self.right {
                c = c.indicator(name.clone(), vals.clone());
            }
            c.to_html()?
        };

        // Each chart is a full HTML document — embed via srcdoc to avoid
        // nested <html> tags. Escape double-quotes inside the attribute value.
        let left_srcdoc = left_html.replace('"', "&quot;");
        let right_srcdoc = right_html.replace('"', "&quot;");

        Ok(format!(
            r#"<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>{title}</title>
  <style>
    * {{ box-sizing: border-box; margin: 0; padding: 0; }}
    body {{ background: #111; color: #eee; font-family: sans-serif; padding: 8px; }}
    h2 {{ margin-bottom: 8px; font-size: 1rem; }}
    .grid {{ display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }}
    iframe {{ width: 100%; height: 520px; border: 1px solid #333; border-radius: 4px; }}
  </style>
</head>
<body>
  <h2>{title}</h2>
  <div class="grid">
    <iframe srcdoc="{left}"></iframe>
    <iframe srcdoc="{right}"></iframe>
  </div>
</body>
</html>"#,
            title = self.title,
            left = left_srcdoc,
            right = right_srcdoc,
        ))
    }

    /// Write both charts as separate PNG files: `{stem}_left.png` and `{stem}_right.png`.
    pub fn to_png_pair(&self, stem: impl AsRef<Path>) -> Result<()> {
        use crate::candle::CandleChart;
        let stem = stem.as_ref();
        if let Some(parent) = stem.parent() {
            std::fs::create_dir_all(parent)?;
        }

        let left_path = stem.with_extension("").with_file_name(format!(
            "{}_left.png",
            stem.file_stem().unwrap_or_default().to_string_lossy()
        ));
        let right_path = stem.with_extension("").with_file_name(format!(
            "{}_right.png",
            stem.file_stem().unwrap_or_default().to_string_lossy()
        ));

        {
            let mut c = CandleChart::new(self.bars).with_title(
                self.left.as_ref().map(|(n, _)| n.as_str()).unwrap_or("left"),
            );
            if let Some((name, vals)) = &self.left {
                c = c.indicator(name.clone(), vals.clone());
            }
            c.to_png(&left_path)?;
        }
        {
            let mut c = CandleChart::new(self.bars).with_title(
                self.right.as_ref().map(|(n, _)| n.as_str()).unwrap_or("right"),
            );
            if let Some((name, vals)) = &self.right {
                c = c.indicator(name.clone(), vals.clone());
            }
            c.to_png(&right_path)?;
        }
        Ok(())
    }
}

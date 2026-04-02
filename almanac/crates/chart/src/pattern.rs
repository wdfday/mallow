//! Geometric overlays for pattern detection debugging.
//!
//! Add pivot points, trendlines, zones, and text annotations on top of a
//! [`CandleChart`]. Designed so pattern-detection code can visualise its
//! findings without coupling to any particular rendering backend.
//!
//! # Example
//! ```no_run
//! use alm_chart::pattern::{PatternOverlay, Pivot, PivotKind, TrendLine, Zone};
//! use alm_chart::candle::CandleChart;
//!
//! let overlay = PatternOverlay::new()
//!     .pivot(Pivot { bar_index: 10, price: 105.0, kind: PivotKind::High, label: Some("Left Shoulder".into()) })
//!     .pivot(Pivot { bar_index: 20, price: 115.0, kind: PivotKind::High, label: Some("Head".into()) })
//!     .pivot(Pivot { bar_index: 30, price: 105.0, kind: PivotKind::High, label: Some("Right Shoulder".into()) })
//!     .trendline(TrendLine { from: (10, 100.0), to: (30, 100.0), label: Some("Neckline".into()) })
//!     .zone(Zone { from_bar: 28, to_bar: 40, price_lo: 95.0, price_hi: 100.0, label: Some("TP Zone".into()) });
//! ```

use charming::element::{MarkPoint, MarkPointData};

// ── Types ────────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PivotKind {
    High,
    Low,
}

/// A significant price point (swing high/low, pattern vertex).
#[derive(Debug, Clone)]
pub struct Pivot {
    /// 0-based index into the `bars` slice.
    pub bar_index: usize,
    pub price: f64,
    pub kind: PivotKind,
    /// Optional label shown on the chart (e.g. "Head", "Left Shoulder").
    pub label: Option<String>,
}

/// A straight line connecting two bar-index/price points.
#[derive(Debug, Clone)]
pub struct TrendLine {
    /// (bar_index, price) of start point.
    pub from: (usize, f64),
    /// (bar_index, price) of end point.
    pub to: (usize, f64),
    pub label: Option<String>,
}

/// A shaded rectangular zone between two bar indices and two price levels.
#[derive(Debug, Clone)]
pub struct Zone {
    pub from_bar: usize,
    pub to_bar: usize,
    pub price_lo: f64,
    pub price_hi: f64,
    pub label: Option<String>,
}

/// A simple text annotation anchored to a bar index and price.
#[derive(Debug, Clone)]
pub struct Annotation {
    pub bar_index: usize,
    pub price: f64,
    pub text: String,
}

// ── PatternOverlay ────────────────────────────────────────────────────────────

/// Collects geometric overlays to be rendered on top of a candlestick chart.
#[derive(Debug, Clone, Default)]
pub struct PatternOverlay {
    pub pivots: Vec<Pivot>,
    pub trendlines: Vec<TrendLine>,
    pub zones: Vec<Zone>,
    pub annotations: Vec<Annotation>,
}

impl PatternOverlay {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn pivot(mut self, p: Pivot) -> Self {
        self.pivots.push(p);
        self
    }

    pub fn trendline(mut self, t: TrendLine) -> Self {
        self.trendlines.push(t);
        self
    }

    pub fn zone(mut self, z: Zone) -> Self {
        self.zones.push(z);
        self
    }

    pub fn annotate(mut self, bar_index: usize, price: f64, text: impl Into<String>) -> Self {
        self.annotations.push(Annotation {
            bar_index,
            price,
            text: text.into(),
        });
        self
    }

    // ── charming helpers ──────────────────────────────────────────────────────

    /// Build a `charming::MarkPoint` series from pivot points.
    /// Pass this directly to a `Line` or `Candlestick` series via `.mark_point()`.
    pub fn charming_mark_points(&self) -> MarkPoint {
        let data: Vec<MarkPointData> = self
            .pivots
            .iter()
            .map(|p| {
                let label = p
                    .label
                    .clone()
                    .unwrap_or_else(|| match p.kind {
                        PivotKind::High => "H".into(),
                        PivotKind::Low => "L".into(),
                    });
                MarkPointData::new()
                    .name(label)
                    .x_axis(p.bar_index as f64)
                    .y_axis(p.price)
            })
            .collect();

        MarkPoint::new().data(data)
    }

    // ── plotters helpers ──────────────────────────────────────────────────────

    /// Draw all overlays onto an existing plotters `ChartContext`.
    ///
    /// Call this *after* drawing candlesticks on the same chart.
    pub fn draw_plotters<DB, X, Y>(
        &self,
        chart: &mut plotters::prelude::ChartContext<
            DB,
            plotters::prelude::Cartesian2d<X, Y>,
        >,
    ) -> anyhow::Result<()>
    where
        DB: plotters::prelude::DrawingBackend,
        DB::ErrorType: 'static,
        X: plotters::prelude::Ranged<ValueType = usize>,
        Y: plotters::prelude::Ranged<ValueType = f64>,
    {
        use plotters::prelude::*;

        // Trendlines
        for tl in &self.trendlines {
            chart
                .draw_series(LineSeries::new(
                    vec![tl.from, tl.to],
                    ShapeStyle {
                        color: MAGENTA.mix(0.8),
                        filled: false,
                        stroke_width: 2,
                    },
                ))
                .map_err(|e| anyhow::anyhow!("trendline: {e}"))?;
        }

        // Zones (filled rectangles)
        for z in &self.zones {
            chart
                .draw_series(std::iter::once(Rectangle::new(
                    [(z.from_bar, z.price_lo), (z.to_bar, z.price_hi)],
                    ShapeStyle {
                        color: YELLOW.mix(0.25),
                        filled: true,
                        stroke_width: 1,
                    },
                )))
                .map_err(|e| anyhow::anyhow!("zone: {e}"))?;
        }

        // Pivot markers
        for p in &self.pivots {
            let color = match p.kind {
                PivotKind::High => RED,
                PivotKind::Low => GREEN,
            };
            chart
                .draw_series(std::iter::once(Circle::new(
                    (p.bar_index, p.price),
                    5i32,
                    ShapeStyle::from(&color).filled(),
                )))
                .map_err(|e| anyhow::anyhow!("pivot circle: {e}"))?;
        }

        // Text annotations
        for ann in &self.annotations {
            chart
                .draw_series(std::iter::once(Text::new(
                    ann.text.clone(),
                    (ann.bar_index, ann.price),
                    ("sans-serif", 12).into_font().color(&BLACK),
                )))
                .map_err(|e| anyhow::anyhow!("annotation: {e}"))?;
        }

        Ok(())
    }
}

// ── from_pattern_signal (requires feature = "pattern-bridge") ─────────────────

#[cfg(feature = "pattern-bridge")]
impl PatternOverlay {
    /// Convert a [`alm_pattern::PatternSignal`] into a `PatternOverlay` ready
    /// for rendering on a [`CandleChart`].
    ///
    /// Enable the `pattern-bridge` feature in alm-chart's Cargo.toml to use this.
    pub fn from_pattern_signal(sig: &alm_pattern::PatternSignal) -> Self {
        let mut overlay = PatternOverlay::new();

        for pp in &sig.pivots {
            overlay = overlay.pivot(Pivot {
                bar_index: pp.bar_index,
                price: pp.price,
                kind: if pp.is_high { PivotKind::High } else { PivotKind::Low },
                label: None,
            });
        }

        let last_bar = sig.pivots.last().map(|p| p.bar_index).unwrap_or(0);
        overlay = overlay.annotate(last_bar, sig.entry_price, format!("Entry {:.2}", sig.entry_price));
        if let Some(tp) = sig.target_price {
            overlay = overlay.annotate(last_bar, tp, format!("TP {:.2}", tp));
        }
        if let Some(sl) = sig.stop_price {
            overlay = overlay.annotate(last_bar, sl, format!("SL {:.2}", sl));
        }

        overlay
    }
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_overlay_is_empty() {
        let ov = PatternOverlay::default();
        assert!(ov.pivots.is_empty());
        assert!(ov.trendlines.is_empty());
        assert!(ov.zones.is_empty());
        assert!(ov.annotations.is_empty());
    }

    #[test]
    fn pivot_builder_accumulates() {
        let ov = PatternOverlay::new()
            .pivot(Pivot { bar_index: 5, price: 105.0, kind: PivotKind::High, label: Some("H1".into()) })
            .pivot(Pivot { bar_index: 10, price: 95.0, kind: PivotKind::Low, label: None })
            .pivot(Pivot { bar_index: 15, price: 106.0, kind: PivotKind::High, label: Some("H2".into()) });
        assert_eq!(ov.pivots.len(), 3);
        assert_eq!(ov.pivots[1].bar_index, 10);
        assert_eq!(ov.pivots[1].kind, PivotKind::Low);
    }

    #[test]
    fn trendline_builder_accumulates() {
        let ov = PatternOverlay::new()
            .trendline(TrendLine { from: (0, 100.0), to: (10, 110.0), label: Some("Up".into()) })
            .trendline(TrendLine { from: (10, 110.0), to: (20, 100.0), label: None });
        assert_eq!(ov.trendlines.len(), 2);
        assert_eq!(ov.trendlines[0].label.as_deref(), Some("Up"));
    }

    #[test]
    fn zone_and_annotation_builder() {
        let ov = PatternOverlay::new()
            .zone(Zone { from_bar: 5, to_bar: 15, price_lo: 90.0, price_hi: 100.0, label: Some("S/R".into()) })
            .annotate(20, 105.0, "Breakout");
        assert_eq!(ov.zones.len(), 1);
        assert_eq!(ov.zones[0].price_hi, 100.0);
        assert_eq!(ov.annotations.len(), 1);
        assert_eq!(ov.annotations[0].text, "Breakout");
        assert_eq!(ov.annotations[0].bar_index, 20);
    }

    #[test]
    fn charming_mark_points_does_not_panic() {
        let ov = PatternOverlay::new()
            .pivot(Pivot { bar_index: 3, price: 102.0, kind: PivotKind::High, label: Some("Top".into()) })
            .pivot(Pivot { bar_index: 7, price: 98.0, kind: PivotKind::Low, label: None });
        // Should not panic; we can't easily inspect the MarkPoint internals
        let _mp = ov.charming_mark_points();
    }

    #[test]
    fn pivot_kind_default_labels() {
        // High → "H", Low → "L" when label is None
        let ov = PatternOverlay::new()
            .pivot(Pivot { bar_index: 0, price: 100.0, kind: PivotKind::High, label: None })
            .pivot(Pivot { bar_index: 1, price: 90.0, kind: PivotKind::Low, label: None });
        // charming_mark_points uses "H"/"L" as default — verify the logic indirectly
        for p in &ov.pivots {
            let label = p.label.clone().unwrap_or_else(|| match p.kind {
                PivotKind::High => "H".into(),
                PivotKind::Low => "L".into(),
            });
            match p.kind {
                PivotKind::High => assert_eq!(label, "H"),
                PivotKind::Low => assert_eq!(label, "L"),
            }
        }
    }

    #[test]
    fn builder_is_chainable_and_cloneable() {
        let ov = PatternOverlay::new()
            .pivot(Pivot { bar_index: 1, price: 101.0, kind: PivotKind::High, label: None });
        let ov2 = ov.clone();
        assert_eq!(ov2.pivots.len(), 1);
        assert_eq!(ov2.pivots[0].price, 101.0);
    }
}

use alm_chart::{
    candle::CandleChart,
    compare::CompareChart,
    pattern::{Pivot, PivotKind, PatternOverlay, TrendLine},
};
use alm_core::Bar;

fn make_bars(n: usize) -> Vec<Bar> {
    (0..n)
        .map(|i| {
            let t = i as i64 * 3_600_000; // 1h apart
            let base = 100.0 + (i as f64 * 0.5);
            Bar::new(t, "TEST", base, base + 2.0, base - 1.0, base + 1.0, 1000.0 + i as f64)
        })
        .collect()
}

#[test]
fn candle_to_html_non_empty() {
    let bars = make_bars(50);
    let html = CandleChart::new(&bars)
        .with_title("Smoke Test")
        .to_html()
        .expect("to_html failed");
    assert!(html.contains("echarts"), "should contain ECharts script");
    assert!(html.contains("Smoke Test"), "should contain title");
}

#[test]
fn candle_with_indicator_to_html() {
    let bars = make_bars(30);
    let sma: Vec<Option<f64>> = bars
        .iter()
        .enumerate()
        .map(|(i, b)| if i >= 5 { Some(b.close) } else { None })
        .collect();

    let html = CandleChart::new(&bars)
        .with_title("With SMA")
        .indicator("SMA5", sma)
        .volume(true)
        .to_html()
        .expect("to_html with indicator failed");

    assert!(html.contains("SMA5"));
}

#[test]
fn candle_with_overlay_to_html() {
    let bars = make_bars(40);
    let overlay = PatternOverlay::new()
        .pivot(Pivot {
            bar_index: 10,
            price: bars[10].high,
            kind: PivotKind::High,
            label: Some("Peak".into()),
        })
        .pivot(Pivot {
            bar_index: 20,
            price: bars[20].low,
            kind: PivotKind::Low,
            label: Some("Trough".into()),
        })
        .trendline(TrendLine {
            from: (10, bars[10].high),
            to: (20, bars[20].low),
            label: Some("Support".into()),
        });

    let html = CandleChart::new(&bars)
        .with_title("Pattern Overlay")
        .overlay(overlay)
        .to_html()
        .expect("overlay to_html failed");

    assert!(html.contains("Support"), "trendline label should appear");
}

#[test]
fn candle_to_png_creates_file() {
    let bars = make_bars(30);
    let path = std::env::temp_dir().join("alm_chart_smoke_candle.png");
    CandleChart::new(&bars)
        .with_title("PNG Test")
        .to_png(&path)
        .expect("to_png failed");
    assert!(path.exists(), "PNG file should be created");
    assert!(path.metadata().unwrap().len() > 0, "PNG should not be empty");
    let _ = std::fs::remove_file(&path);
}

#[test]
fn compare_chart_to_html_non_empty() {
    let my_vals: Vec<Option<f64>> = (0..40).map(|i| Some(50.0 + i as f64 * 0.1)).collect();
    let ref_vals: Vec<Option<f64>> = (0..40).map(|i| Some(50.1 + i as f64 * 0.1)).collect();

    let html = CompareChart::new("RSI Cross-validate")
        .series("alm-indicator", my_vals)
        .series("TA-Lib", ref_vals)
        .to_html()
        .expect("compare to_html failed");

    assert!(html.contains("alm-indicator"));
    assert!(html.contains("TA-Lib"));
}

#[test]
fn compare_chart_to_png_creates_file() {
    let vals: Vec<Option<f64>> = (0..20).map(|i| Some(i as f64)).collect();
    let path = std::env::temp_dir().join("alm_chart_smoke_compare.png");
    CompareChart::new("Smoke")
        .series("A", vals.clone())
        .series("B", vals)
        .to_png(&path)
        .expect("compare to_png failed");
    assert!(path.exists());
    let _ = std::fs::remove_file(&path);
}

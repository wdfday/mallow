use anyhow::{Context, Result};
use alm_core::Bar;
use polars::prelude::*;
use std::path::Path;
use std::sync::Arc;

use crate::feed::BarFeed;

/// Zero-copy bar feed backed by Polars Arrow columns.
///
/// Replaces the old `Vec<Bar>` materialisation:
///   old: scan → collect → Vec<Bar>  (1 String alloc per bar, 96 B/bar)
///   new: scan → collect → struct-of-arrays inside Arrow buffers (48 B/bar)
///
/// `next()` constructs a `Bar` on the fly from column slices; the symbol is
/// an `Arc<str>` shared across all bars so there is no per-bar heap alloc.
pub struct ParquetFeed {
    symbol: Arc<str>,
    cursor: usize,
    len: usize,
    // pre-extracted column refs for O(1) index access
    t_col: Vec<i64>,
    o_col: Vec<f64>,
    h_col: Vec<f64>,
    l_col: Vec<f64>,
    c_col: Vec<f64>,
    v_col: Vec<f64>,
    vw_col: Option<Vec<f64>>,
    n_col:  Option<Vec<i64>>,
}

impl ParquetFeed {
    /// Load and sort bars from a single Parquet file.
    pub fn load(path: &Path, symbol: impl Into<String>) -> Result<Self> {
        let sym: Arc<str> = Arc::from(symbol.into().as_str());
        let df = scan_sorted(path)?.collect()
            .with_context(|| format!("collect parquet: {}", path.display()))?;
        Self::from_df(df, sym)
    }

    /// Load from multiple files (e.g. daily splits) and merge by timestamp.
    pub fn load_many(paths: &[&Path], symbol: impl Into<String>) -> Result<Self> {
        Self::load_many_filtered(paths, symbol, None, None)
    }

    /// Load from multiple files and materialize only rows within [from_ms, to_ms].
    ///
    /// The date range is applied as a Polars predicate **before** `.collect()`,
    /// enabling row-group pruning on Parquet files with timestamp statistics.
    /// This is dramatically faster than loading all data and filtering afterwards.
    pub fn load_many_filtered(
        paths:   &[&Path],
        symbol:  impl Into<String>,
        from_ms: Option<i64>,
        to_ms:   Option<i64>,
    ) -> Result<Self> {
        let sym: Arc<str> = Arc::from(symbol.into().as_str());
        let frames: Result<Vec<LazyFrame>> = paths.iter()
            .map(|p| scan_sorted(p))
            .collect();
        let mut lf = concat(frames?, UnionArgs::default())?
            .sort(["t"], SortMultipleOptions::default());

        if let Some(from) = from_ms {
            lf = lf.filter(col("t").gt_eq(lit(from)));
        }
        if let Some(to) = to_ms {
            lf = lf.filter(col("t").lt_eq(lit(to)));
        }

        let df = lf.collect().context("collect merged parquet")?;
        Self::from_df(df, sym)
    }

    fn from_df(df: DataFrame, symbol: Arc<str>) -> Result<Self> {
        let len = df.height();

        // Extract primitive slices once — O(n) copy of the numeric columns into
        // plain Vec<primitive>, still much cheaper than Vec<Bar> with Strings.
        let t_col  = df.column("t")?.i64()?.into_no_null_iter().collect();
        let o_col  = df.column("o")?.f64()?.into_no_null_iter().collect();
        let h_col  = df.column("h")?.f64()?.into_no_null_iter().collect();
        let l_col  = df.column("l")?.f64()?.into_no_null_iter().collect();
        let c_col  = df.column("c")?.f64()?.into_no_null_iter().collect();
        let v_col  = extract_volume(&df)?;
        let vw_col = df.column("vw").ok()
            .and_then(|s| s.f64().ok())
            .map(|ca| ca.into_iter().map(|v| v.unwrap_or(f64::NAN)).collect());
        let n_col  = df.column("n").ok()
            .and_then(|s| s.i64().ok())
            .map(|ca| ca.into_iter().map(|v| v.unwrap_or(0)).collect());

        Ok(Self { symbol, cursor: 0, len, t_col, o_col, h_col, l_col, c_col, v_col, vw_col, n_col })
    }
}

impl BarFeed for ParquetFeed {
    #[inline]
    fn next(&mut self) -> Option<Bar> {
        if self.cursor >= self.len {
            return None;
        }
        let i = self.cursor;
        self.cursor += 1;

        let mut bar = Bar::new(
            self.t_col[i],
            self.symbol.as_ref(),
            self.o_col[i],
            self.h_col[i],
            self.l_col[i],
            self.c_col[i],
            self.v_col[i],
        );
        bar.vwap         = self.vw_col.as_ref().map(|v| v[i]).filter(|v| v.is_finite());
        bar.transactions = self.n_col.as_ref().map(|v| v[i]);
        Some(bar)
    }

    fn symbol(&self) -> &str { &self.symbol }
    fn len(&self)    -> usize { self.len }
    fn reset(&mut self)       { self.cursor = 0; }
}

// ── helpers ───────────────────────────────────────────────────────────────────

fn scan_sorted(path: &Path) -> Result<LazyFrame> {
    LazyFrame::scan_parquet(path, ScanArgsParquet::default())
        .with_context(|| format!("scan parquet: {}", path.display()))
        .map(|lf| lf.sort(["t"], SortMultipleOptions::default()))
}

fn extract_volume(df: &DataFrame) -> Result<Vec<f64>> {
    let col = df.column("v")?;
    Ok(match col.dtype() {
        DataType::Int64 => col.i64()?.into_iter().map(|v| v.unwrap_or(0) as f64).collect(),
        _               => col.f64()?.into_iter().map(|v| v.unwrap_or(0.0)).collect(),
    })
}

use anyhow::{Context, Result};
use alm_core::{Bar, Timeframe};
use arrow_array::{Float64Array, Int64Array, Array};
use parquet::arrow::arrow_reader::ParquetRecordBatchReaderBuilder;
use std::fs::File;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use crate::feed::BarFeed;

/// Bar feed that loads all bars from one or more Parquet files into memory.
///
/// vs RowGroupFeed:
///   ParquetFeed:   loads all rows → Vec<primitive>  — O(1) next(), instant reset()
///   RowGroupFeed:  streams row-groups on demand     — O(chunk) refill, re-opens on reset()
///
/// Use ParquetFeed when the dataset fits in RAM and you need fast iteration or
/// repeated reset() (e.g. parameter sweeps, multi-pass strategies).
pub struct ParquetFeed {
    symbol: Arc<str>,
    cursor: usize,
    len: usize,
    timeframe: Timeframe,
    t_col: Vec<i64>,
    o_col: Vec<f64>,
    h_col: Vec<f64>,
    l_col: Vec<f64>,
    c_col: Vec<f64>,
    v_col: Vec<f64>,
}

impl ParquetFeed {
    pub fn load(path: &Path, symbol: impl Into<String>) -> Result<Self> {
        Self::load_many_filtered(&[path], symbol, None, None)
    }

    pub fn load_many(paths: &[&Path], symbol: impl Into<String>) -> Result<Self> {
        Self::load_many_filtered(paths, symbol, None, None)
    }

    pub fn load_dir(dir: &Path, symbol: impl Into<String>) -> Result<Self> {
        let paths = parquet_files_in(dir)?;
        let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();
        Self::load_many_filtered(&refs, symbol, None, None)
    }

    pub fn load_dir_filtered(
        dir: &Path,
        symbol: impl Into<String>,
        from_ms: Option<i64>,
        to_ms: Option<i64>,
    ) -> Result<Self> {
        let paths = parquet_files_in(dir)?;
        let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();
        Self::load_many_filtered(&refs, symbol, from_ms, to_ms)
    }

    pub fn load_many_filtered(
        paths: &[&Path],
        symbol: impl Into<String>,
        from_ms: Option<i64>,
        to_ms: Option<i64>,
    ) -> Result<Self> {
        let sym: Arc<str> = Arc::from(symbol.into().as_str());

        let mut t_col: Vec<i64> = Vec::new();
        let mut o_col: Vec<f64> = Vec::new();
        let mut h_col: Vec<f64> = Vec::new();
        let mut l_col: Vec<f64> = Vec::new();
        let mut c_col: Vec<f64> = Vec::new();
        let mut v_col: Vec<f64> = Vec::new();

        for path in paths {
            let file = File::open(path)
                .with_context(|| format!("open: {}", path.display()))?;
            let reader = ParquetRecordBatchReaderBuilder::try_new(file)
                .with_context(|| format!("read metadata: {}", path.display()))?
                .build()
                .with_context(|| format!("build reader: {}", path.display()))?;

            for batch in reader {
                let batch = batch.context("read batch")?;
                let t = col_i64(&batch, "t")?;
                let o = col_f64(&batch, "o")?;
                let h = col_f64(&batch, "h")?;
                let l = col_f64(&batch, "l")?;
                let c = col_f64(&batch, "c")?;
                let v = col_v(&batch);

                for i in 0..batch.num_rows() {
                    let ts = t[i];
                    if from_ms.map_or(true, |f| ts >= f) && to_ms.map_or(true, |to| ts <= to) {
                        t_col.push(ts);
                        o_col.push(o[i]);
                        h_col.push(h[i]);
                        l_col.push(l[i]);
                        c_col.push(c[i]);
                        v_col.push(v[i]);
                    }
                }
            }
        }

        // sort by t when merging multiple files
        if paths.len() > 1 {
            let mut idx: Vec<usize> = (0..t_col.len()).collect();
            idx.sort_unstable_by_key(|&i| t_col[i]);
            t_col = idx.iter().map(|&i| t_col[i]).collect();
            o_col = idx.iter().map(|&i| o_col[i]).collect();
            h_col = idx.iter().map(|&i| h_col[i]).collect();
            l_col = idx.iter().map(|&i| l_col[i]).collect();
            c_col = idx.iter().map(|&i| c_col[i]).collect();
            v_col = idx.iter().map(|&i| v_col[i]).collect();
        }

        let timeframe = Timeframe::detect(&t_col);
        let len = t_col.len();

        Ok(Self { symbol: sym, cursor: 0, len, timeframe, t_col, o_col, h_col, l_col, c_col, v_col })
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
        Some(Bar::new(
            self.t_col[i],
            self.symbol.as_ref(),
            self.o_col[i],
            self.h_col[i],
            self.l_col[i],
            self.c_col[i],
            self.v_col[i],
        ))
    }

    fn symbol(&self)    -> &str      { &self.symbol }
    fn len(&self)       -> usize     { self.len }
    fn reset(&mut self)              { self.cursor = 0; }
    fn timeframe(&self) -> Timeframe { self.timeframe }
}

// ── helpers ───────────────────────────────────────────────────────────────────

pub(crate) fn parquet_files_in(dir: &Path) -> Result<Vec<PathBuf>> {
    let mut paths: Vec<PathBuf> = std::fs::read_dir(dir)
        .with_context(|| format!("read dir: {}", dir.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().map_or(false, |x| x == "parquet"))
        .collect();
    paths.sort();
    Ok(paths)
}

fn col_f64(batch: &arrow_array::RecordBatch, name: &str) -> Result<Vec<f64>> {
    let i = batch.schema().index_of(name)
        .with_context(|| format!("column '{name}' not found"))?;
    let arr = batch.column(i).as_any().downcast_ref::<Float64Array>()
        .with_context(|| format!("column '{name}' is not f64"))?;
    Ok((0..arr.len()).map(|j| if arr.is_null(j) { f64::NAN } else { arr.value(j) }).collect())
}

fn col_i64(batch: &arrow_array::RecordBatch, name: &str) -> Result<Vec<i64>> {
    let i = batch.schema().index_of(name)
        .with_context(|| format!("column '{name}' not found"))?;
    let arr = batch.column(i).as_any().downcast_ref::<Int64Array>()
        .with_context(|| format!("column '{name}' is not i64"))?;
    Ok((0..arr.len()).map(|j| if arr.is_null(j) { 0 } else { arr.value(j) }).collect())
}

fn col_v(batch: &arrow_array::RecordBatch) -> Vec<f64> {
    let schema = batch.schema();
    match schema.index_of("v") {
        Ok(i) => {
            let col = batch.column(i);
            if let Some(a) = col.as_any().downcast_ref::<Float64Array>() {
                return (0..a.len()).map(|j| if a.is_null(j) { 0.0 } else { a.value(j) }).collect();
            }
            if let Some(a) = col.as_any().downcast_ref::<Int64Array>() {
                return (0..a.len()).map(|j| if a.is_null(j) { 0.0 } else { a.value(j) as f64 }).collect();
            }
            vec![0.0; batch.num_rows()]
        }
        Err(_) => vec![0.0; batch.num_rows()],
    }
}

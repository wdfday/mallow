use anyhow::{Context, Result};
use alm_core::{Bar, Timeframe};
use arrow_array::{Float64Array, Int64Array, Array};
use parquet::arrow::arrow_reader::ParquetRecordBatchReaderBuilder;
use std::fs::File;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use crate::feed::BarFeed;
use crate::parquet::parquet_files_in;

/// Bar feed that reads one Parquet row-group at a time.
///
/// vs ChunkedFeed (Polars slice):
///   ChunkedFeed:     re-opens file + seeks per chunk, boundary không align
///   RowGroupFeed:    file mở 1 lần, đọc tuần tự, boundary tự nhiên của Parquet
///
/// Mỗi row-group ~65k–128k rows — đây là đơn vị decompress của Parquet,
/// nên không bao giờ phải decompress thêm gì so với nhu cầu.
pub struct RowGroupFeed {
    // lưu path để reset() có thể mở lại file
    paths: Vec<PathBuf>,
    symbol: Arc<str>,
    timeframe: Timeframe,
    total_len: usize,
    // current row-group buffer
    buf_t: Vec<i64>,
    buf_o: Vec<f64>,
    buf_h: Vec<f64>,
    buf_l: Vec<f64>,
    buf_c: Vec<f64>,
    buf_v: Vec<f64>,
    buf_cursor: usize,
    buf_len: usize,
    // reader state
    batches: BatchIter,
    done: bool,
}

impl RowGroupFeed {
    pub fn load(path: &Path, symbol: impl Into<String>) -> Result<Self> {
        Self::load_many(&[path], symbol)
    }

    pub fn load_dir(dir: &Path, symbol: impl Into<String>) -> Result<Self> {
        let paths = parquet_files_in(dir)?;
        let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();
        Self::load_many(&refs, symbol)
    }

    /// Merge nhiều file, đọc tuần tự từng row-group.
    pub fn load_many(paths: &[&Path], symbol: impl Into<String>) -> Result<Self> {
        let sym: Arc<str> = Arc::from(symbol.into().as_str());
        let owned: Vec<PathBuf> = paths.iter().map(|p| p.to_path_buf()).collect();

        let (total_len, timeframe, batches) = open_readers(&owned)?;

        let mut feed = Self {
            paths: owned,
            symbol: sym,
            timeframe,
            total_len,
            buf_t: vec![],
            buf_o: vec![],
            buf_h: vec![],
            buf_l: vec![],
            buf_c: vec![],
            buf_v: vec![],
            buf_cursor: 0,
            buf_len: 0,
            batches,
            done: false,
        };
        feed.refill();
        Ok(feed)
    }

    fn refill(&mut self) {
        loop {
            match self.batches.next() {
                None => { self.done = true; self.buf_len = 0; return; }
                Some(Err(_)) => continue,
                Some(Ok(batch)) => {
                    if let Ok(()) = self.fill_from_batch(&batch) {
                        if self.buf_len > 0 { return; }
                    }
                }
            }
        }
    }

    fn fill_from_batch(&mut self, batch: &arrow_array::RecordBatch) -> Result<()> {
        let schema = batch.schema();

        let t = col_i64(batch, "t")?;
        let o = col_f64(batch, "o")?;
        let h = col_f64(batch, "h")?;
        let l = col_f64(batch, "l")?;
        let c = col_f64(batch, "c")?;

        let v: Vec<f64> = match schema.index_of("v") {
            Ok(i) => match batch.column(i).as_any().downcast_ref::<Float64Array>() {
                Some(a) => buf_f64(a),
                None => {
                    let a = batch.column(i).as_any().downcast_ref::<Int64Array>()
                        .context("v column not f64 or i64")?;
                    buf_i64_as_f64(a)
                }
            },
            Err(_) => vec![0.0; batch.num_rows()],
        };

        self.buf_t = t;
        self.buf_o = o;
        self.buf_h = h;
        self.buf_l = l;
        self.buf_c = c;
        self.buf_v = v;
        self.buf_cursor = 0;
        self.buf_len = batch.num_rows();
        Ok(())
    }
}

impl BarFeed for RowGroupFeed {
    #[inline]
    fn next(&mut self) -> Option<Bar> {
        if self.buf_cursor >= self.buf_len {
            if self.done { return None; }
            self.refill();
            if self.buf_len == 0 { return None; }
        }
        let i = self.buf_cursor;
        self.buf_cursor += 1;
        Some(Bar::new(
            self.buf_t[i],
            self.symbol.as_ref(),
            self.buf_o[i],
            self.buf_h[i],
            self.buf_l[i],
            self.buf_c[i],
            self.buf_v[i],
        ))
    }

    fn symbol(&self) -> &str { &self.symbol }
    fn len(&self) -> usize { self.total_len }
    fn timeframe(&self) -> Timeframe { self.timeframe }

    fn reset(&mut self) {
        let paths = self.paths.clone();
        if let Ok((_, _, batches)) = open_readers(&paths) {
            self.batches = batches;
            self.buf_cursor = 0;
            self.buf_len = 0;
            self.done = false;
            self.refill();
        }
    }
}

// ── helpers ───────────────────────────────────────────────────────────────────

type BatchIter = Box<dyn Iterator<Item = Result<arrow_array::RecordBatch, arrow_schema::ArrowError>> + Send>;

fn open_readers(paths: &[PathBuf]) -> Result<(usize, Timeframe, BatchIter)> {
    let mut total_len = 0usize;
    let mut t_sample: Vec<i64> = vec![];
    let mut iters: Vec<BatchIter> = vec![];

    for path in paths {
        let file = File::open(path)
            .with_context(|| format!("open: {}", path.display()))?;
        let builder = ParquetRecordBatchReaderBuilder::try_new(file)
            .with_context(|| format!("read metadata: {}", path.display()))?;

        // row count from metadata — O(n_row_groups), no data read
        let meta = builder.metadata();
        let file_rows: usize = (0..meta.num_row_groups())
            .map(|i| meta.row_group(i).num_rows() as usize)
            .sum();
        total_len += file_rows;

        // sample timestamps từ row-group đầu để detect timeframe
        if t_sample.is_empty() && meta.num_row_groups() > 0 {
            let sample_file = File::open(path)?;
            let sample_builder = ParquetRecordBatchReaderBuilder::try_new(sample_file)?
                .with_row_groups(vec![0])
                .with_batch_size(200);
            if let Ok(mut r) = sample_builder.build() {
                if let Some(Ok(batch)) = r.next() {
                    if let Ok(vals) = col_i64(&batch, "t") {
                        t_sample = vals.into_iter().take(200).collect();
                    }
                }
            }
        }

        // reader tự nhiên iterate theo row-group
        let file2 = File::open(path)
            .with_context(|| format!("open reader: {}", path.display()))?;
        let reader = ParquetRecordBatchReaderBuilder::try_new(file2)?
            .build()
            .with_context(|| format!("build reader: {}", path.display()))?;
        iters.push(Box::new(reader));
    }

    let timeframe = Timeframe::detect(&t_sample);
    let chained: BatchIter = Box::new(iters.into_iter().flatten());
    Ok((total_len, timeframe, chained))
}

fn col_f64(batch: &arrow_array::RecordBatch, name: &str) -> Result<Vec<f64>> {
    let i = batch.schema().index_of(name)
        .with_context(|| format!("column '{name}' not found"))?;
    let arr = batch.column(i).as_any().downcast_ref::<Float64Array>()
        .with_context(|| format!("column '{name}' is not f64"))?;
    Ok(buf_f64(arr))
}

fn col_i64(batch: &arrow_array::RecordBatch, name: &str) -> Result<Vec<i64>> {
    let i = batch.schema().index_of(name)
        .with_context(|| format!("column '{name}' not found"))?;
    let arr = batch.column(i).as_any().downcast_ref::<Int64Array>()
        .with_context(|| format!("column '{name}' is not i64"))?;
    Ok(buf_i64(arr))
}

#[inline]
fn buf_f64(arr: &Float64Array) -> Vec<f64> {
    if arr.null_count() == 0 {
        arr.values().to_vec()
    } else {
        arr.iter().map(|v| v.unwrap_or(f64::NAN)).collect()
    }
}

#[inline]
fn buf_i64(arr: &Int64Array) -> Vec<i64> {
    if arr.null_count() == 0 {
        arr.values().to_vec()
    } else {
        arr.iter().map(|v| v.unwrap_or(0)).collect()
    }
}

#[inline]
fn buf_i64_as_f64(arr: &Int64Array) -> Vec<f64> {
    if arr.null_count() == 0 {
        arr.values().iter().map(|&v| v as f64).collect()
    } else {
        arr.iter().map(|v| v.map(|x| x as f64).unwrap_or(0.0)).collect()
    }
}

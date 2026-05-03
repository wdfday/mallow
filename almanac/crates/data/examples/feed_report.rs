/// Feed report — in bảng text + sinh HTML chart (dark theme, Chart.js).
///
/// Run:
///   cargo run -p alm-data --example feed_report --release
///
/// Output: target/feed_report.html  (tự mở browser)
use std::path::{Path, PathBuf};
use std::time::Instant;

use alm_data::{BarFeed, ParquetFeed, RowGroupFeed};

// ── OS metrics ────────────────────────────────────────────────────────────────

fn cpu_ms() -> f64 {
    #[cfg(unix)]
    unsafe {
        let mut u = std::mem::zeroed::<libc::rusage>();
        libc::getrusage(libc::RUSAGE_SELF, &mut u);
        let user = u.ru_utime.tv_sec as f64 * 1_000.0 + u.ru_utime.tv_usec as f64 / 1_000.0;
        let sys  = u.ru_stime.tv_sec as f64 * 1_000.0 + u.ru_stime.tv_usec as f64 / 1_000.0;
        user + sys
    }
    #[cfg(not(unix))]
    { 0.0 }
}

fn rss_mib() -> f64 {
    #[cfg(target_os = "linux")]
    {
        std::fs::read_to_string("/proc/self/status").ok()
            .and_then(|s| s.lines().find(|l| l.starts_with("VmRSS:"))
                .and_then(|l| l.split_whitespace().nth(1))
                .and_then(|n| n.parse::<f64>().ok()))
            .map(|kb| kb / 1024.0).unwrap_or(0.0)
    }
    #[cfg(target_os = "macos")]
    {
        let pid = std::process::id().to_string();
        std::process::Command::new("ps").args(["-o", "rss=", "-p", &pid])
            .output().ok()
            .and_then(|o| String::from_utf8(o.stdout).ok())
            .and_then(|s| s.trim().parse::<f64>().ok())
            .map(|kb| kb / 1024.0).unwrap_or(0.0)
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    { 0.0 }
}

// ── data structs ──────────────────────────────────────────────────────────────

#[derive(Clone)]
struct Metrics {
    wall_ms: f64,
    cpu_ms:  f64,
    ram_mib: f64,
}

#[derive(Clone)]
struct Row {
    name:   String,
    n_bars: usize,
    load:   Metrics,
    drain:  Metrics,
}

impl Row {
    fn throughput_m(&self) -> f64 {
        if self.drain.wall_ms > 0.0 { self.n_bars as f64 / self.drain.wall_ms / 1_000.0 } else { 0.0 }
    }
}

struct Section {
    title: String,
    rows:  Vec<Row>,
}

// ── measurement ───────────────────────────────────────────────────────────────

fn measure(make: impl FnOnce() -> Box<dyn BarFeed>) -> Row {
    // load
    let rss0 = rss_mib(); let cpu0 = cpu_ms(); let t0 = Instant::now();
    let mut feed = make();
    let load = Metrics {
        wall_ms: t0.elapsed().as_secs_f64() * 1_000.0,
        cpu_ms:  cpu_ms() - cpu0,
        ram_mib: rss_mib() - rss0,
    };
    let n_bars = feed.len();

    // drain
    let rss1 = rss_mib(); let cpu1 = cpu_ms(); let t1 = Instant::now();
    let mut n = 0usize;
    while feed.next().is_some() { n += 1; }
    let drain = Metrics {
        wall_ms: t1.elapsed().as_secs_f64() * 1_000.0,
        cpu_ms:  cpu_ms() - cpu1,
        ram_mib: rss_mib() - rss1,
    };

    Row { name: String::new(), n_bars: if n_bars > 0 { n_bars } else { n }, load, drain }
}

fn row(name: &str, make: impl FnOnce() -> Box<dyn BarFeed>) -> Row {
    let mut r = measure(make);
    r.name = name.to_string();
    r
}

// ── data helpers ──────────────────────────────────────────────────────────────

fn tf_dir(tf: &str) -> PathBuf {
    Path::new("testdata/BTCUSDT").join(tf)
}

fn tf_paths(tf: &str) -> Vec<PathBuf> {
    let dir = tf_dir(tf);
    let mut v: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap_or_else(|_| panic!("missing {}", dir.display()))
        .filter_map(|e| e.ok()).map(|e| e.path())
        .filter(|p| p.extension().map_or(false, |x| x == "parquet"))
        .collect();
    v.sort(); v
}

fn bulk(tf: &str) -> PathBuf { tf_paths(tf).into_iter().next().unwrap() }

// ── sections ──────────────────────────────────────────────────────────────────

fn section_tf(tf: &str) -> Section {
    let path  = bulk(tf);
    let dir   = tf_dir(tf);
    let paths = tf_paths(tf);
    let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();
    let n = ParquetFeed::load(&path, "BTCUSDT").unwrap().len();

    let rows = vec![
        row("ParquetFeed/single",    || Box::new(ParquetFeed::load(&path, "BTCUSDT").unwrap())),
        row("ParquetFeed/many",      || Box::new(ParquetFeed::load_many(&refs, "BTCUSDT").unwrap())),
        row("ParquetFeed/dir",       || Box::new(ParquetFeed::load_dir(&dir, "BTCUSDT").unwrap())),
        row("RowGroupFeed/single",   || Box::new(RowGroupFeed::load(&path, "BTCUSDT").unwrap())),
        row("RowGroupFeed/many",     || Box::new(RowGroupFeed::load_many(&refs, "BTCUSDT").unwrap())),
        row("RowGroupFeed/dir",      || Box::new(RowGroupFeed::load_dir(&dir, "BTCUSDT").unwrap())),
    ];

    Section { title: format!("BTCUSDT {tf} — {n} bars"), rows }
}

// ── text output ───────────────────────────────────────────────────────────────

fn print_section(s: &Section) {
    println!("\n  {}", s.title);
    println!("  {}", "─".repeat(s.title.len()));
    println!("  {:<26} │ {:>10} │ {:>10} │ {:>10} │ {:>10} │ {:>10} │ {:>8}",
        "Feed", "load wall", "load cpu", "load RAM", "drain wall", "drain cpu", "Mbar/s");
    println!("  {}", "─".repeat(110));
    for r in &s.rows {
        println!("  {:<26} │ {:>8.0} ms │ {:>8.0} ms │ {:>+7.1} MiB │ {:>8.0} ms │ {:>8.0} ms │ {:>7.2}",
            r.name,
            r.load.wall_ms, r.load.cpu_ms, r.load.ram_mib,
            r.drain.wall_ms, r.drain.cpu_ms,
            r.throughput_m());
    }
}

// ── HTML output ───────────────────────────────────────────────────────────────

fn js_array(vals: &[f64]) -> String {
    format!("[{}]", vals.iter().map(|v| format!("{v:.2}")).collect::<Vec<_>>().join(", "))
}

fn js_labels(rows: &[Row]) -> String {
    format!("[{}]", rows.iter().map(|r| format!("\"{}\"", r.name)).collect::<Vec<_>>().join(", "))
}

fn html_section(s: &Section, idx: usize) -> String {
    let labels     = js_labels(&s.rows);
    let load_wall  = js_array(&s.rows.iter().map(|r| r.load.wall_ms).collect::<Vec<_>>());
    let load_cpu   = js_array(&s.rows.iter().map(|r| r.load.cpu_ms).collect::<Vec<_>>());
    let load_ram   = js_array(&s.rows.iter().map(|r| r.load.ram_mib).collect::<Vec<_>>());
    let drain_wall = js_array(&s.rows.iter().map(|r| r.drain.wall_ms).collect::<Vec<_>>());
    let drain_cpu  = js_array(&s.rows.iter().map(|r| r.drain.cpu_ms).collect::<Vec<_>>());
    let throughput = js_array(&s.rows.iter().map(|r| r.throughput_m()).collect::<Vec<_>>());
    let title = &s.title;
    let i = idx;

    format!(r#"
<section>
  <h2>{title}</h2>
  <div class="grid4">
    <div class="card"><h3>Load time (ms)</h3><canvas id="lt{i}"></canvas></div>
    <div class="card"><h3>Load RAM Δ (MiB)</h3><canvas id="lr{i}"></canvas></div>
    <div class="card"><h3>Drain time (ms)</h3><canvas id="dt{i}"></canvas></div>
    <div class="card"><h3>Throughput (Mbar/s)</h3><canvas id="tp{i}"></canvas></div>
  </div>
</section>
<script>
(function(){{
  const lbl = {labels};
  const opts = {{ responsive: true, plugins: {{ legend: {{ labels: {{ color:'#cdd9e5' }} }} }},
    scales: {{ x: {{ ticks: {{ color:'#768390' }}, grid: {{ color:'#21262d' }} }},
               y: {{ ticks: {{ color:'#768390' }}, grid: {{ color:'#21262d' }} }} }} }};
  new Chart(document.getElementById('lt{i}'), {{ type:'bar',
    data: {{ labels: lbl, datasets: [
      {{ label:'wall', data:{load_wall}, backgroundColor:'#388bfd99', borderColor:'#388bfd', borderWidth:1 }},
      {{ label:'cpu',  data:{load_cpu},  backgroundColor:'#f7853599', borderColor:'#f78535', borderWidth:1 }}
    ]}}, options: opts }});
  new Chart(document.getElementById('lr{i}'), {{ type:'bar',
    data: {{ labels: lbl, datasets: [
      {{ label:'RAM Δ MiB', data:{load_ram}, backgroundColor:'#3fb95099', borderColor:'#3fb950', borderWidth:1 }}
    ]}}, options: opts }});
  new Chart(document.getElementById('dt{i}'), {{ type:'bar',
    data: {{ labels: lbl, datasets: [
      {{ label:'wall', data:{drain_wall}, backgroundColor:'#388bfd99', borderColor:'#388bfd', borderWidth:1 }},
      {{ label:'cpu',  data:{drain_cpu},  backgroundColor:'#f7853599', borderColor:'#f78535', borderWidth:1 }}
    ]}}, options: opts }});
  new Chart(document.getElementById('tp{i}'), {{ type:'bar',
    data: {{ labels: lbl, datasets: [
      {{ label:'Mbar/s', data:{throughput}, backgroundColor:'#a371f799', borderColor:'#a371f7', borderWidth:1 }}
    ]}}, options: opts }});
}})();
</script>
"#)
}

fn write_html(sections: &[Section]) -> PathBuf {
    let body: String = sections.iter().enumerate()
        .map(|(i, s)| html_section(s, i))
        .collect();

    let html = format!(r#"<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Feed Report — BTCUSDT</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js"></script>
<style>
  * {{ box-sizing: border-box; margin: 0; padding: 0; }}
  body {{ background: #0d1117; color: #cdd9e5; font-family: ui-monospace, monospace; padding: 24px; }}
  h1   {{ color: #58a6ff; margin-bottom: 8px; font-size: 1.4em; }}
  p.sub {{ color: #768390; font-size: .85em; margin-bottom: 24px; }}
  h2   {{ color: #79c0ff; margin: 32px 0 12px; font-size: 1.1em; border-bottom: 1px solid #21262d; padding-bottom: 6px; }}
  h3   {{ color: #adbac7; font-size: .8em; margin-bottom: 8px; }}
  .grid4 {{ display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }}
  .card  {{ background: #161b22; border: 1px solid #21262d; border-radius: 8px; padding: 16px; }}
  section {{ margin-bottom: 8px; }}
  @media(min-width:1200px) {{ .grid4 {{ grid-template-columns: 1fr 1fr 1fr 1fr; }} }}
</style>
</head>
<body>
<h1>Feed Report — BTCUSDT</h1>
<p class="sub">
  <b style="color:#388bfd">blue</b> = wall time &nbsp;|&nbsp;
  <b style="color:#f78535">orange</b> = cpu time &nbsp;|&nbsp;
  <b style="color:#3fb950">green</b> = RAM delta &nbsp;|&nbsp;
  <b style="color:#a371f7">purple</b> = throughput
  &nbsp;&nbsp;•&nbsp;&nbsp; wall − cpu = I/O wait
</p>
{body}
</body></html>"#);

    let out = PathBuf::from("../target/feed_report.html");
    std::fs::write(&out, html).expect("write HTML");
    out
}

fn open_browser(path: &Path) {
    #[cfg(target_os = "macos")]
    { let _ = std::process::Command::new("open").arg(path).spawn(); }
    #[cfg(target_os = "linux")]
    { let _ = std::process::Command::new("xdg-open").arg(path).spawn(); }
    #[cfg(target_os = "windows")]
    { let _ = std::process::Command::new("cmd").args(["/c", "start", path.to_str().unwrap()]).spawn(); }
}

// ── main ──────────────────────────────────────────────────────────────────────

fn main() {
    println!("\n  Feed Report — BTCUSDT (collecting…)");

    let sections = vec![
        section_tf("M1"),
        section_tf("H1"),
        section_tf("D1"),
    ];

    println!();
    println!("  ╔══════════════════════════════════════════════════════════════════╗");
    println!("  ║  wall − cpu = I/O wait  |  load RAM Δ = heap allocated at load ║");
    println!("  ╚══════════════════════════════════════════════════════════════════╝");
    for s in &sections { print_section(s); }

    let html_path = write_html(&sections);
    println!("\n  → HTML report: {}", html_path.display());
    open_browser(&html_path);
}

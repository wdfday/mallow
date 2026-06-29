const { ChartState } = require('../pkg-node/alm_wasm.js');
const { Bench } = require('tinybench');
const path = require('path');

// Load real candles from the generated candles.json file
const candlesPath = path.join(__dirname, 'candles.json');
const candles = require(candlesPath);

const nBars = candles.length;
const t = candles.map(item => item.t);
const o = candles.map(item => item.o);
const h = candles.map(item => item.h);
const l = candles.map(item => item.l);
const c = candles.map(item => item.c);
const v = candles.map(item => item.v);

console.log(`=== alm-wasm Node.js Benchmark (Statistical Analysis) ===`);
console.log(`Loaded ${nBars} real candles from alm-data (BTCUSDT M1).`);

// Setup ChartState
const state = new ChartState("BTCUSDT");
state.load_tail(t, o, h, l, c, v);

// ── Benchmark 1: Simple EMA Crossover Strategy ───────────────────────────────
const simpleScript = `
  let ema9  = ind.ema(9);
  let ema21 = ind.ema(21);
  if cross_above(ema9, ema21) {
      entry = true;
  }
  if cross_below(ema9, ema21) {
      exit = true;
  }
`;

// ── Benchmark 2: Complex Bollinger Band + RSI + ADX + ATR Mean Reversion ──────
const complexScript = `
  let bb    = ind.bbands(20, buf=3);
  let rsi5  = ind.rsi(5);
  let adx14 = ind.adx(14);
  let atr14 = ind.atr(14);

  let is_ranging = adx14[0] < 25.0;

  if is_ranging && close[0] < bb[0].lower && rsi5[0] < 25.0 {
      entry     = true;
      tp        = bb[0].middle;
      sl        = close[0] - atr14[0];
      strength  = (25.0 - rsi5[0]) / 25.0;
      reason    = "bb_lower_bounce";
  }

  if close[0] >= bb[0].middle || rsi5[0] > 70.0 {
      exit = true;
  }
`;

const btConfig = JSON.stringify({
  initial_capital: 10000,
  position_size_pct: 1.0,
  commission_pct: 0.001,
  slippage_pct: 0.0005
});

// Setup tinybench (each task runs for at least 2000ms)
const bench = new Bench({ time: 2000 });

bench
  .add('Simple Strategy (EMA Crossover)', () => {
    state.backtest(simpleScript, btConfig, 0, 0);
  })
  .add('Complex Strategy (BB + RSI + ADX + ATR Mean Reversion)', () => {
    state.backtest(complexScript, btConfig, 0, 0);
  });

(async () => {
  console.log("Running benchmarks (this might take a few seconds)...");
  await bench.run();

  console.log("\nBenchmark Results:");
  console.table(bench.table());

  // Print throughput for each task
  for (const task of bench.tasks) {
    const avgTimeMs = task.result.period; 
    const opsPerSec = task.result.throughput.mean;
    const barsPerSec = opsPerSec * nBars;
    
    console.log(`\nTask: ${task.name}`);
    console.log(`  Average Time   : ${(avgTimeMs).toFixed(2)} ms (sd: ±${task.result.latency.sd.toFixed(2)} ms)`);
    console.log(`  Throughput     : ${barsPerSec.toLocaleString(undefined, { maximumFractionDigits: 0 })} bars/sec`);
    console.log(`  Margin of Error: ±${(task.result.latency.rme || 0).toFixed(2)}%`);
    console.log(`  p50 / p99      : ${task.result.latency.p50.toFixed(2)} ms / ${task.result.latency.p99.toFixed(2)} ms`);
  }
})();

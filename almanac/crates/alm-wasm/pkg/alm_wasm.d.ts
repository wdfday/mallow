/* tslint:disable */
/* eslint-disable */

/**
 * Stateful chart object — holds bars + live indicator instances.
 *
 * Create via `ChartState::new(symbol)`, push bars, then read indicator series.
 * Backtest runs on-demand; it never fires automatically on bar mutations.
 */
export class ChartState {
    free(): void;
    [Symbol.dispose](): void;
    /**
     * Prepend a single bar (historical pagination). Resets and replays all
     * indicator instances from scratch. O(n × indicators).
     */
    add_head(t: number, o: number, h: number, l: number, c: number, v: number): any;
    /**
     * Add a single indicator. Replays it through existing bars without touching
     * other slots. O(n) for this indicator only.
     *
     * `config_json`: single indicator spec, e.g. `{"type":"ema","period":20,"label":"ema20"}`
     *
     * Returns `null` on success, `{error}` on failure.
     */
    add_indicator(config_json: string): any;
    /**
     * Append a single bar. Feeds it through the candle transform (if any)
     * incrementally, then through existing indicator instances. O(indicators).
     */
    add_tail(t: number, o: number, h: number, l: number, c: number, v: number): any;
    /**
     * Run a script backtest over the current bar series.
     *
     * `config_json`: `{"initial_capital":10000,"position_size_pct":1.0,...}`
     */
    backtest(script: string, config_json: string): any;
    /**
     * Run a named-strategy backtest over the current bar series.
     */
    backtest_named(strategy_name: string, params_json: string, config_json: string): any;
    /**
     * Returns the number of displayable bars (post-transform).
     */
    bar_count(): number;
    /**
     * Remove the current script; named indicator slots are unaffected.
     */
    clear_script(): void;
    /**
     * Prepend many bars (historical pagination). Resets and replays everything.
     * O(n × indicators).
     */
    load_head(t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array): any;
    /**
     * Append many bars at once. Feeds each through the transform incrementally.
     * O(new × indicators).
     */
    load_tail(t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array): any;
    constructor(symbol: string);
    /**
     * Returns the number of raw bars loaded.
     */
    raw_bar_count(): number;
    /**
     * Remove the indicator with the given label. O(1). No-op if not found.
     */
    remove_indicator(label: string): void;
    /**
     * Clear everything — bars, indicator instances, configs, and script.
     */
    reset(): void;
    /**
     * Clear bars and reset all indicator instances; keep configs and script.
     */
    reset_bars(): void;
    /**
     * Set the candle transform applied before indicator computation.
     *
     * `kind`: `"raw"` | `"ha"` | `"smooth_ha"`
     * `period`: EMA smoothing period for `"smooth_ha"` (ignored otherwise).
     *
     * Triggers a full indicator replay with transformed bars. O(n × indicators).
     */
    set_candle_transform(kind: string, period: number): any;
    /**
     * Set indicator configs. Replaces the previous set, replays all current bars.
     *
     * `config_json`: JSON array of indicator specs, e.g.
     * `[{"type":"ema","period":20,"label":"ema20"}, {"type":"rsi","period":14}]`
     *
     * Returns `null` on success, `{error}` on parse/validate failure.
     */
    set_indicators(config_json: string): any;
    /**
     * Set a Rhai script for indicator-only evaluation.
     *
     * Indicator series are auto-extracted from `ind.TYPE(period)` declarations
     * exactly as the herald `/api/v1/data` endpoint does. Signal outputs
     * (`long`/`short`/`exit` + TP/SL) are collected into the snapshot's
     * `signals` array as bars stream in.
     *
     * The script/backtest ALWAYS evaluate on **raw** OHLCV, independent of the
     * chart-level candle transform (HA toggle is display-only). This keeps the
     * on-chart strategy result identical to the deep backtest. The script's own
     * `candle.transform` directive is handled internally by `ScriptStrategy` on
     * the raw bars — that's separate from the display toggle.
     *
     * Replays all current bars through the script. O(n).
     * Returns `null` on success, `{error}` on script compile failure.
     */
    set_script(script: string): any;
    /**
     * Return the current snapshot. Reads from cached series — O(1) build.
     * Useful after `set_indicators` to get initial state before any bar arrives.
     */
    snapshot(): any;
    symbol(): string;
    /**
     * Update the config of an existing indicator by label. Replays only that
     * slot through all bars. O(n × 1). No-op (returns error) if label not found.
     *
     * `config_json`: new indicator spec — the label field is ignored, the
     * existing slot's label is kept.
     */
    update_indicator(label: string, config_json: string): any;
}

/**
 * Standard Heikin-Ashi. Returns `{t,o,h,l,c,v}` same length as input.
 */
export function heikin_ashi(t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array): any;

/**
 * Full indicator catalog for editor hints: `[{name, label, category, description,
 * params:[{name,type,default}], outputs:[{name,type}]}, ...]`.
 */
export function indicator_catalog(): any;

export function init(): void;

/**
 * List all indicator type strings accepted by `run_indicators`.
 */
export function list_indicators(): any;

/**
 * List all multi-TF named strategy keys usable with `run_mtf_backtest`.
 */
export function list_mtf_strategies(): any;

/**
 * List all single-TF named strategy keys usable with `ChartState::backtest_named`.
 */
export function list_strategies(): any;

/**
 * Compute indicators over a bar series.
 *
 * `config_json`: `{ label: { "type": "ema", "period": 20, ... } }`
 * Returns: `{ label: { field: (number|null)[] } }`
 */
export function run_indicators(symbol: string, t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array, config_json: string): any;

/**
 * EMA-smoothed Heikin-Ashi (Smoothed HA).
 *
 * Mỗi thành phần OHLC được EMA(period)-smooth độc lập trước khi tính HA.
 * Warmup = `period` bar (first `period-1` bars trả về None, bị drop khỏi output).
 * FE phải dùng mảng `t` trả về để align — output ngắn hơn input `period-1` bar.
 *
 * `period` phải >= 2. Truyền `period=1` là lỗi — dùng `heikin_ashi()` cho HA chuẩn.
 */
export function smooth_ha(t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array, period: number): any;

/**
 * Lint a strategy script client-side (no server round-trip).
 *
 * Returns `{ errors: [{line, col, message, severity}], scope: {...} }`.
 * `base_tf`: `"M1"` / `"H4"` / etc., or empty string to skip TF checks.
 * Supports all 13 timeframes: M1 M3 M5 M10 M15 M30 H1 H2 H4 H6 H12 D1 W1.
 */
export function validate_script(script: string, base_tf: string): any;

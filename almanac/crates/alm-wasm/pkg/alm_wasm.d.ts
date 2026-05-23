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
     * (`long`/`short`/`exit`) are evaluated internally but discarded.
     *
     * The script's own `candle.transform` directive is honoured independently
     * of the chart-level candle transform — script always operates on raw bars.
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

export function init(): void;

/**
 * List all indicator type strings accepted by `run_indicators`.
 */
export function list_indicators(): any;

/**
 * List all named strategy keys usable with `run_backtest`.
 */
export function list_strategies(): any;

/**
 * Run a named strategy backtest client-side.
 *
 * `strategy_name`: any name from `list_strategies()` (e.g. `"ema_cross"`, `"rsi_mean_rev"`)
 * `params_json`:   `{"period": 14, ...}`
 * `config_json`:   `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
 */
export function run_backtest(symbol: string, strategy_name: string, params_json: string, t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array, config_json: string): any;

/**
 * Compute indicators over a bar series.
 *
 * `config_json`: `{ label: { "type": "ema", "period": 20, ... } }`
 * Returns: `{ label: { field: (number|null)[] } }`
 */
export function run_indicators(symbol: string, t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array, config_json: string): any;

/**
 * Run a script backtest client-side.
 *
 * `script`: Script (same syntax as herald `/api/v1/backtest/script`)
 * `config_json`: `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
 */
export function run_script_backtest(symbol: string, script: string, t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array, config_json: string): any;

/**
 * EMA-smoothed Heikin-Ashi. Warmup bars trimmed from output.
 */
export function smooth_ha(t: Float64Array, o: Float64Array, h: Float64Array, l: Float64Array, c: Float64Array, v: Float64Array, period: number): any;

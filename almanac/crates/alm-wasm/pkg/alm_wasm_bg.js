/**
 * Stateful chart object — holds bars + live indicator instances.
 *
 * Create via `ChartState::new(symbol)`, push bars, then read indicator series.
 * Backtest runs on-demand; it never fires automatically on bar mutations.
 */
export class ChartState {
    __destroy_into_raw() {
        const ptr = this.__wbg_ptr;
        this.__wbg_ptr = 0;
        ChartStateFinalization.unregister(this);
        return ptr;
    }
    free() {
        const ptr = this.__destroy_into_raw();
        wasm.__wbg_chartstate_free(ptr, 0);
    }
    /**
     * Prepend a single bar (historical pagination). Resets and replays all
     * indicator instances from scratch. O(n × indicators).
     * @param {number} t
     * @param {number} o
     * @param {number} h
     * @param {number} l
     * @param {number} c
     * @param {number} v
     * @returns {any}
     */
    add_head(t, o, h, l, c, v) {
        const ret = wasm.chartstate_add_head(this.__wbg_ptr, t, o, h, l, c, v);
        return ret;
    }
    /**
     * Add a single indicator. Replays it through existing bars without touching
     * other slots. O(n) for this indicator only.
     *
     * `config_json`: single indicator spec, e.g. `{"type":"ema","period":20,"label":"ema20"}`
     *
     * Returns `null` on success, `{error}` on failure.
     * @param {string} config_json
     * @returns {any}
     */
    add_indicator(config_json) {
        const ptr0 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_add_indicator(this.__wbg_ptr, ptr0, len0);
        return ret;
    }
    /**
     * Append a single bar. Feeds it through the candle transform (if any)
     * incrementally, then through existing indicator instances. O(indicators).
     * @param {number} t
     * @param {number} o
     * @param {number} h
     * @param {number} l
     * @param {number} c
     * @param {number} v
     * @returns {any}
     */
    add_tail(t, o, h, l, c, v) {
        const ret = wasm.chartstate_add_tail(this.__wbg_ptr, t, o, h, l, c, v);
        return ret;
    }
    /**
     * Run a script backtest over the current bar series.
     *
     * `config_json`: `{"initial_capital":10000,"position_size_pct":1.0,...}`
     * @param {string} script
     * @param {string} config_json
     * @returns {any}
     */
    backtest(script, config_json) {
        const ptr0 = passStringToWasm0(script, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len1 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_backtest(this.__wbg_ptr, ptr0, len0, ptr1, len1);
        return ret;
    }
    /**
     * Run a named-strategy backtest over the current bar series.
     * @param {string} strategy_name
     * @param {string} params_json
     * @param {string} config_json
     * @returns {any}
     */
    backtest_named(strategy_name, params_json, config_json) {
        const ptr0 = passStringToWasm0(strategy_name, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passStringToWasm0(params_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len1 = WASM_VECTOR_LEN;
        const ptr2 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len2 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_backtest_named(this.__wbg_ptr, ptr0, len0, ptr1, len1, ptr2, len2);
        return ret;
    }
    /**
     * Returns the number of displayable bars (post-transform).
     * @returns {number}
     */
    bar_count() {
        const ret = wasm.chartstate_bar_count(this.__wbg_ptr);
        return ret >>> 0;
    }
    /**
     * Remove the current script; named indicator slots are unaffected.
     */
    clear_script() {
        wasm.chartstate_clear_script(this.__wbg_ptr);
    }
    /**
     * Prepend many bars (historical pagination). Resets and replays everything.
     * O(n × indicators).
     * @param {Float64Array} t
     * @param {Float64Array} o
     * @param {Float64Array} h
     * @param {Float64Array} l
     * @param {Float64Array} c
     * @param {Float64Array} v
     * @returns {any}
     */
    load_head(t, o, h, l, c, v) {
        const ptr0 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
        const len1 = WASM_VECTOR_LEN;
        const ptr2 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
        const len2 = WASM_VECTOR_LEN;
        const ptr3 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
        const len3 = WASM_VECTOR_LEN;
        const ptr4 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
        const len4 = WASM_VECTOR_LEN;
        const ptr5 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
        const len5 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_load_head(this.__wbg_ptr, ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5);
        return ret;
    }
    /**
     * Append many bars at once. Feeds each through the transform incrementally.
     * O(new × indicators).
     * @param {Float64Array} t
     * @param {Float64Array} o
     * @param {Float64Array} h
     * @param {Float64Array} l
     * @param {Float64Array} c
     * @param {Float64Array} v
     * @returns {any}
     */
    load_tail(t, o, h, l, c, v) {
        const ptr0 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
        const len1 = WASM_VECTOR_LEN;
        const ptr2 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
        const len2 = WASM_VECTOR_LEN;
        const ptr3 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
        const len3 = WASM_VECTOR_LEN;
        const ptr4 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
        const len4 = WASM_VECTOR_LEN;
        const ptr5 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
        const len5 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_load_tail(this.__wbg_ptr, ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5);
        return ret;
    }
    /**
     * @param {string} symbol
     */
    constructor(symbol) {
        const ptr0 = passStringToWasm0(symbol, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_new(ptr0, len0);
        this.__wbg_ptr = ret >>> 0;
        ChartStateFinalization.register(this, this.__wbg_ptr, this);
        return this;
    }
    /**
     * Returns the number of raw bars loaded.
     * @returns {number}
     */
    raw_bar_count() {
        const ret = wasm.chartstate_raw_bar_count(this.__wbg_ptr);
        return ret >>> 0;
    }
    /**
     * Remove the indicator with the given label. O(1). No-op if not found.
     * @param {string} label
     */
    remove_indicator(label) {
        const ptr0 = passStringToWasm0(label, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        wasm.chartstate_remove_indicator(this.__wbg_ptr, ptr0, len0);
    }
    /**
     * Clear everything — bars, indicator instances, configs, and script.
     */
    reset() {
        wasm.chartstate_reset(this.__wbg_ptr);
    }
    /**
     * Clear bars and reset all indicator instances; keep configs and script.
     */
    reset_bars() {
        wasm.chartstate_reset_bars(this.__wbg_ptr);
    }
    /**
     * Set the candle transform applied before indicator computation.
     *
     * `kind`: `"raw"` | `"ha"` | `"smooth_ha"`
     * `period`: EMA smoothing period for `"smooth_ha"` (ignored otherwise).
     *
     * Triggers a full indicator replay with transformed bars. O(n × indicators).
     * @param {string} kind
     * @param {number} period
     * @returns {any}
     */
    set_candle_transform(kind, period) {
        const ptr0 = passStringToWasm0(kind, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_set_candle_transform(this.__wbg_ptr, ptr0, len0, period);
        return ret;
    }
    /**
     * Set indicator configs. Replaces the previous set, replays all current bars.
     *
     * `config_json`: JSON array of indicator specs, e.g.
     * `[{"type":"ema","period":20,"label":"ema20"}, {"type":"rsi","period":14}]`
     *
     * Returns `null` on success, `{error}` on parse/validate failure.
     * @param {string} config_json
     * @returns {any}
     */
    set_indicators(config_json) {
        const ptr0 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_set_indicators(this.__wbg_ptr, ptr0, len0);
        return ret;
    }
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
     * @param {string} script
     * @returns {any}
     */
    set_script(script) {
        const ptr0 = passStringToWasm0(script, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_set_script(this.__wbg_ptr, ptr0, len0);
        return ret;
    }
    /**
     * Return the current snapshot. Reads from cached series — O(1) build.
     * Useful after `set_indicators` to get initial state before any bar arrives.
     * @returns {any}
     */
    snapshot() {
        const ret = wasm.chartstate_snapshot(this.__wbg_ptr);
        return ret;
    }
    /**
     * @returns {string}
     */
    symbol() {
        let deferred1_0;
        let deferred1_1;
        try {
            const ret = wasm.chartstate_symbol(this.__wbg_ptr);
            deferred1_0 = ret[0];
            deferred1_1 = ret[1];
            return getStringFromWasm0(ret[0], ret[1]);
        } finally {
            wasm.__wbindgen_free(deferred1_0, deferred1_1, 1);
        }
    }
    /**
     * Update the config of an existing indicator by label. Replays only that
     * slot through all bars. O(n × 1). No-op (returns error) if label not found.
     *
     * `config_json`: new indicator spec — the label field is ignored, the
     * existing slot's label is kept.
     * @param {string} label
     * @param {string} config_json
     * @returns {any}
     */
    update_indicator(label, config_json) {
        const ptr0 = passStringToWasm0(label, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len0 = WASM_VECTOR_LEN;
        const ptr1 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
        const len1 = WASM_VECTOR_LEN;
        const ret = wasm.chartstate_update_indicator(this.__wbg_ptr, ptr0, len0, ptr1, len1);
        return ret;
    }
}
if (Symbol.dispose) ChartState.prototype[Symbol.dispose] = ChartState.prototype.free;

/**
 * Standard Heikin-Ashi. Returns `{t,o,h,l,c,v}` same length as input.
 * @param {Float64Array} t
 * @param {Float64Array} o
 * @param {Float64Array} h
 * @param {Float64Array} l
 * @param {Float64Array} c
 * @param {Float64Array} v
 * @returns {any}
 */
export function heikin_ashi(t, o, h, l, c, v) {
    const ptr0 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
    const len2 = WASM_VECTOR_LEN;
    const ptr3 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
    const len3 = WASM_VECTOR_LEN;
    const ptr4 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
    const len4 = WASM_VECTOR_LEN;
    const ptr5 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
    const len5 = WASM_VECTOR_LEN;
    const ret = wasm.heikin_ashi(ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5);
    return ret;
}

export function init() {
    wasm.init();
}

/**
 * List all indicator type strings accepted by `run_indicators`.
 * @returns {any}
 */
export function list_indicators() {
    const ret = wasm.list_indicators();
    return ret;
}

/**
 * List all named strategy keys usable with `run_backtest`.
 * @returns {any}
 */
export function list_strategies() {
    const ret = wasm.list_strategies();
    return ret;
}

/**
 * Run a named strategy backtest client-side.
 *
 * `strategy_name`: any name from `list_strategies()` (e.g. `"ema_cross"`, `"rsi_mean_rev"`)
 * `params_json`:   `{"period": 14, ...}`
 * `config_json`:   `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
 * @param {string} symbol
 * @param {string} strategy_name
 * @param {string} params_json
 * @param {Float64Array} t
 * @param {Float64Array} o
 * @param {Float64Array} h
 * @param {Float64Array} l
 * @param {Float64Array} c
 * @param {Float64Array} v
 * @param {string} config_json
 * @returns {any}
 */
export function run_backtest(symbol, strategy_name, params_json, t, o, h, l, c, v, config_json) {
    const ptr0 = passStringToWasm0(symbol, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passStringToWasm0(strategy_name, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passStringToWasm0(params_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len2 = WASM_VECTOR_LEN;
    const ptr3 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
    const len3 = WASM_VECTOR_LEN;
    const ptr4 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
    const len4 = WASM_VECTOR_LEN;
    const ptr5 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
    const len5 = WASM_VECTOR_LEN;
    const ptr6 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
    const len6 = WASM_VECTOR_LEN;
    const ptr7 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
    const len7 = WASM_VECTOR_LEN;
    const ptr8 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
    const len8 = WASM_VECTOR_LEN;
    const ptr9 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len9 = WASM_VECTOR_LEN;
    const ret = wasm.run_backtest(ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5, ptr6, len6, ptr7, len7, ptr8, len8, ptr9, len9);
    return ret;
}

/**
 * Compute indicators over a bar series.
 *
 * `config_json`: `{ label: { "type": "ema", "period": 20, ... } }`
 * Returns: `{ label: { field: (number|null)[] } }`
 * @param {string} symbol
 * @param {Float64Array} t
 * @param {Float64Array} o
 * @param {Float64Array} h
 * @param {Float64Array} l
 * @param {Float64Array} c
 * @param {Float64Array} v
 * @param {string} config_json
 * @returns {any}
 */
export function run_indicators(symbol, t, o, h, l, c, v, config_json) {
    const ptr0 = passStringToWasm0(symbol, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
    const len2 = WASM_VECTOR_LEN;
    const ptr3 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
    const len3 = WASM_VECTOR_LEN;
    const ptr4 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
    const len4 = WASM_VECTOR_LEN;
    const ptr5 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
    const len5 = WASM_VECTOR_LEN;
    const ptr6 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
    const len6 = WASM_VECTOR_LEN;
    const ptr7 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len7 = WASM_VECTOR_LEN;
    const ret = wasm.run_indicators(ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5, ptr6, len6, ptr7, len7);
    return ret;
}

/**
 * Run a script backtest client-side.
 *
 * `script`: Script (same syntax as herald `/api/v1/backtest/script`)
 * `config_json`: `{"initial_capital": 10000, "position_size_pct": 1.0, ...}`
 * @param {string} symbol
 * @param {string} script
 * @param {Float64Array} t
 * @param {Float64Array} o
 * @param {Float64Array} h
 * @param {Float64Array} l
 * @param {Float64Array} c
 * @param {Float64Array} v
 * @param {string} config_json
 * @returns {any}
 */
export function run_script_backtest(symbol, script, t, o, h, l, c, v, config_json) {
    const ptr0 = passStringToWasm0(symbol, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passStringToWasm0(script, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
    const len2 = WASM_VECTOR_LEN;
    const ptr3 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
    const len3 = WASM_VECTOR_LEN;
    const ptr4 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
    const len4 = WASM_VECTOR_LEN;
    const ptr5 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
    const len5 = WASM_VECTOR_LEN;
    const ptr6 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
    const len6 = WASM_VECTOR_LEN;
    const ptr7 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
    const len7 = WASM_VECTOR_LEN;
    const ptr8 = passStringToWasm0(config_json, wasm.__wbindgen_malloc, wasm.__wbindgen_realloc);
    const len8 = WASM_VECTOR_LEN;
    const ret = wasm.run_script_backtest(ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5, ptr6, len6, ptr7, len7, ptr8, len8);
    return ret;
}

/**
 * EMA-smoothed Heikin-Ashi. Warmup bars trimmed from output.
 * @param {Float64Array} t
 * @param {Float64Array} o
 * @param {Float64Array} h
 * @param {Float64Array} l
 * @param {Float64Array} c
 * @param {Float64Array} v
 * @param {number} period
 * @returns {any}
 */
export function smooth_ha(t, o, h, l, c, v, period) {
    const ptr0 = passArrayF64ToWasm0(t, wasm.__wbindgen_malloc);
    const len0 = WASM_VECTOR_LEN;
    const ptr1 = passArrayF64ToWasm0(o, wasm.__wbindgen_malloc);
    const len1 = WASM_VECTOR_LEN;
    const ptr2 = passArrayF64ToWasm0(h, wasm.__wbindgen_malloc);
    const len2 = WASM_VECTOR_LEN;
    const ptr3 = passArrayF64ToWasm0(l, wasm.__wbindgen_malloc);
    const len3 = WASM_VECTOR_LEN;
    const ptr4 = passArrayF64ToWasm0(c, wasm.__wbindgen_malloc);
    const len4 = WASM_VECTOR_LEN;
    const ptr5 = passArrayF64ToWasm0(v, wasm.__wbindgen_malloc);
    const len5 = WASM_VECTOR_LEN;
    const ret = wasm.smooth_ha(ptr0, len0, ptr1, len1, ptr2, len2, ptr3, len3, ptr4, len4, ptr5, len5, period);
    return ret;
}
export function __wbg_Error_2e59b1b37a9a34c3(arg0, arg1) {
    const ret = Error(getStringFromWasm0(arg0, arg1));
    return ret;
}
export function __wbg___wbindgen_is_string_b29b5c5a8065ba1a(arg0) {
    const ret = typeof(arg0) === 'string';
    return ret;
}
export function __wbg___wbindgen_is_undefined_c0cca72b82b86f4d(arg0) {
    const ret = arg0 === undefined;
    return ret;
}
export function __wbg___wbindgen_throw_81fc77679af83bc6(arg0, arg1) {
    throw new Error(getStringFromWasm0(arg0, arg1));
}
export function __wbg_getRandomValues_3f44b700395062e5() { return handleError(function (arg0, arg1) {
    globalThis.crypto.getRandomValues(getArrayU8FromWasm0(arg0, arg1));
}, arguments); }
export function __wbg_new_4f9fafbb3909af72() {
    const ret = new Object();
    return ret;
}
export function __wbg_new_99cabae501c0a8a0() {
    const ret = new Map();
    return ret;
}
export function __wbg_new_f3c9df4f38f3f798() {
    const ret = new Array();
    return ret;
}
export function __wbg_now_e7c6795a7f81e10f(arg0) {
    const ret = arg0.now();
    return ret;
}
export function __wbg_performance_3fcf6e32a7e1ed0a(arg0) {
    const ret = arg0.performance;
    return ret;
}
export function __wbg_set_08463b1df38a7e29(arg0, arg1, arg2) {
    const ret = arg0.set(arg1, arg2);
    return ret;
}
export function __wbg_set_6be42768c690e380(arg0, arg1, arg2) {
    arg0[arg1] = arg2;
}
export function __wbg_set_6c60b2e8ad0e9383(arg0, arg1, arg2) {
    arg0[arg1 >>> 0] = arg2;
}
export function __wbg_static_accessor_GLOBAL_THIS_a1248013d790bf5f() {
    const ret = typeof globalThis === 'undefined' ? null : globalThis;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
}
export function __wbg_static_accessor_GLOBAL_f2e0f995a21329ff() {
    const ret = typeof global === 'undefined' ? null : global;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
}
export function __wbg_static_accessor_SELF_24f78b6d23f286ea() {
    const ret = typeof self === 'undefined' ? null : self;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
}
export function __wbg_static_accessor_WINDOW_59fd959c540fe405() {
    const ret = typeof window === 'undefined' ? null : window;
    return isLikeNone(ret) ? 0 : addToExternrefTable0(ret);
}
export function __wbindgen_cast_0000000000000001(arg0) {
    // Cast intrinsic for `F64 -> Externref`.
    const ret = arg0;
    return ret;
}
export function __wbindgen_cast_0000000000000002(arg0) {
    // Cast intrinsic for `I64 -> Externref`.
    const ret = arg0;
    return ret;
}
export function __wbindgen_cast_0000000000000003(arg0, arg1) {
    // Cast intrinsic for `Ref(String) -> Externref`.
    const ret = getStringFromWasm0(arg0, arg1);
    return ret;
}
export function __wbindgen_cast_0000000000000004(arg0) {
    // Cast intrinsic for `U64 -> Externref`.
    const ret = BigInt.asUintN(64, arg0);
    return ret;
}
export function __wbindgen_init_externref_table() {
    const table = wasm.__wbindgen_externrefs;
    const offset = table.grow(4);
    table.set(0, undefined);
    table.set(offset + 0, undefined);
    table.set(offset + 1, null);
    table.set(offset + 2, true);
    table.set(offset + 3, false);
}
const ChartStateFinalization = (typeof FinalizationRegistry === 'undefined')
    ? { register: () => {}, unregister: () => {} }
    : new FinalizationRegistry(ptr => wasm.__wbg_chartstate_free(ptr >>> 0, 1));

function addToExternrefTable0(obj) {
    const idx = wasm.__externref_table_alloc();
    wasm.__wbindgen_externrefs.set(idx, obj);
    return idx;
}

function getArrayU8FromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    return getUint8ArrayMemory0().subarray(ptr / 1, ptr / 1 + len);
}

let cachedFloat64ArrayMemory0 = null;
function getFloat64ArrayMemory0() {
    if (cachedFloat64ArrayMemory0 === null || cachedFloat64ArrayMemory0.byteLength === 0) {
        cachedFloat64ArrayMemory0 = new Float64Array(wasm.memory.buffer);
    }
    return cachedFloat64ArrayMemory0;
}

function getStringFromWasm0(ptr, len) {
    ptr = ptr >>> 0;
    return decodeText(ptr, len);
}

let cachedUint8ArrayMemory0 = null;
function getUint8ArrayMemory0() {
    if (cachedUint8ArrayMemory0 === null || cachedUint8ArrayMemory0.byteLength === 0) {
        cachedUint8ArrayMemory0 = new Uint8Array(wasm.memory.buffer);
    }
    return cachedUint8ArrayMemory0;
}

function handleError(f, args) {
    try {
        return f.apply(this, args);
    } catch (e) {
        const idx = addToExternrefTable0(e);
        wasm.__wbindgen_exn_store(idx);
    }
}

function isLikeNone(x) {
    return x === undefined || x === null;
}

function passArrayF64ToWasm0(arg, malloc) {
    const ptr = malloc(arg.length * 8, 8) >>> 0;
    getFloat64ArrayMemory0().set(arg, ptr / 8);
    WASM_VECTOR_LEN = arg.length;
    return ptr;
}

function passStringToWasm0(arg, malloc, realloc) {
    if (realloc === undefined) {
        const buf = cachedTextEncoder.encode(arg);
        const ptr = malloc(buf.length, 1) >>> 0;
        getUint8ArrayMemory0().subarray(ptr, ptr + buf.length).set(buf);
        WASM_VECTOR_LEN = buf.length;
        return ptr;
    }

    let len = arg.length;
    let ptr = malloc(len, 1) >>> 0;

    const mem = getUint8ArrayMemory0();

    let offset = 0;

    for (; offset < len; offset++) {
        const code = arg.charCodeAt(offset);
        if (code > 0x7F) break;
        mem[ptr + offset] = code;
    }
    if (offset !== len) {
        if (offset !== 0) {
            arg = arg.slice(offset);
        }
        ptr = realloc(ptr, len, len = offset + arg.length * 3, 1) >>> 0;
        const view = getUint8ArrayMemory0().subarray(ptr + offset, ptr + len);
        const ret = cachedTextEncoder.encodeInto(arg, view);

        offset += ret.written;
        ptr = realloc(ptr, len, offset, 1) >>> 0;
    }

    WASM_VECTOR_LEN = offset;
    return ptr;
}

let cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });
cachedTextDecoder.decode();
const MAX_SAFARI_DECODE_BYTES = 2146435072;
let numBytesDecoded = 0;
function decodeText(ptr, len) {
    numBytesDecoded += len;
    if (numBytesDecoded >= MAX_SAFARI_DECODE_BYTES) {
        cachedTextDecoder = new TextDecoder('utf-8', { ignoreBOM: true, fatal: true });
        cachedTextDecoder.decode();
        numBytesDecoded = len;
    }
    return cachedTextDecoder.decode(getUint8ArrayMemory0().subarray(ptr, ptr + len));
}

const cachedTextEncoder = new TextEncoder();

if (!('encodeInto' in cachedTextEncoder)) {
    cachedTextEncoder.encodeInto = function (arg, view) {
        const buf = cachedTextEncoder.encode(arg);
        view.set(buf);
        return {
            read: arg.length,
            written: buf.length
        };
    };
}

let WASM_VECTOR_LEN = 0;


let wasm;
export function __wbg_set_wasm(val) {
    wasm = val;
}

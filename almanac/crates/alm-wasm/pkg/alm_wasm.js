/* @ts-self-types="./alm_wasm.d.ts" */

import * as wasm from "./alm_wasm_bg.wasm";
import { __wbg_set_wasm } from "./alm_wasm_bg.js";
__wbg_set_wasm(wasm);
wasm.__wbindgen_start();
export {
    ChartState, heikin_ashi, indicator_catalog, init, list_indicators, list_mtf_strategies, list_strategies, run_backtest, run_indicators, run_mtf_backtest, run_mtf_script_backtest, run_script_backtest, smooth_ha, validate_script
} from "./alm_wasm_bg.js";

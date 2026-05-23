/* @ts-self-types="./alm_wasm.d.ts" */

import * as wasm from "./alm_wasm_bg.wasm";
import { __wbg_set_wasm } from "./alm_wasm_bg.js";
__wbg_set_wasm(wasm);
wasm.__wbindgen_start();
export {
    ChartState, heikin_ashi, init, list_indicators, list_strategies, run_backtest, run_indicators, run_script_backtest, smooth_ha
} from "./alm_wasm_bg.js";

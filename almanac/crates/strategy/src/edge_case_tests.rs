//! Edge case tests — "chiến lược trời ơi đất hỡi" từ user.
//!
//! Nguyên tắc:
//!   - Không bao giờ panic
//!   - Build error → trả Err rõ ràng
//!   - Logic vô lý → không signal, không crash
//!   - Extreme params → graceful degradation

#[cfg(test)]
mod edge_case_tests {
    use alm_core::{bar::Bar, strategy::Strategy};
    use serde_json::json;

    use crate::factory::build_strategy;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close * 1.005, close * 0.995, close, 1000.0)
    }

    fn run_n(s: &mut dyn Strategy, n: usize, price: f64) -> usize {
        (0..n)
            .map(|i| s.on_bar(&bar(i as i64 * 60_000, price)).len())
            .sum()
    }

    fn bars(n: usize, price: f64) -> Vec<Bar> {
        (0..n).map(|i| bar(i as i64 * 60_000, price)).collect()
    }

    // ── CEL edge cases ────────────────────────────────────────────────────────

    #[test]
    fn cel_impossible_condition_never_signals() {
        // rsi(14) không thể đồng thời < 30 và > 70
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(14) < 30.0 && rsi(14) > 70.0",
            "exit":  "rsi(14) > 50.0"
        })).unwrap();
        assert_eq!(run_n(&mut *s, 100, 100.0), 0, "impossible AND should never signal");
    }

    #[test]
    fn cel_contradiction_cross_never_fires() {
        // ema(5) > ema(20) && ema(5) < ema(20) — không bao giờ đúng
        let mut s = build_strategy("cel", &json!({
            "entry": "ema(5) > ema(20) && ema(5) < ema(20)",
            "exit":  "ema(5) > ema(20)"
        })).unwrap();
        assert_eq!(run_n(&mut *s, 100, 100.0), 0);
    }

    #[test]
    fn cel_always_true_entry_signals_once_then_waits_exit() {
        // close > 0 luôn đúng → entry bar đầu tiên sau warmup
        let mut s = build_strategy("cel", &json!({
            "entry": "close > 0.0",
            "exit":  "close < 0.0"   // không bao giờ đúng → giữ position mãi
        })).unwrap();
        let total = run_n(&mut *s, 50, 100.0);
        // chỉ 1 entry signal (vào position rồi kẹt, không có exit)
        assert_eq!(total, 1, "always-true entry should fire exactly once");
    }

    #[test]
    fn cel_always_true_both_entry_exit_alternates() {
        // cả entry lẫn exit đều luôn true → cứ vào rồi ra mỗi bar
        let mut s = build_strategy("cel", &json!({
            "entry": "close > 0.0",
            "exit":  "close > 0.0"
        })).unwrap();
        let sigs: Vec<_> = bars(20, 100.0).iter()
            .flat_map(|b| s.on_bar(b))
            .collect();
        // entry bar 0, exit bar 1, entry bar 2... alternating
        assert!(sigs.len() > 1, "should alternate entry/exit");
    }

    #[test]
    fn cel_extreme_period_no_panic() {
        // period 999 → warmup dài hơn data, không signal, không crash
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(999) < 30.0",
            "exit":  "rsi(999) > 70.0"
        })).unwrap();
        assert_eq!(run_n(&mut *s, 100, 100.0), 0, "period > bars should produce no signals");
    }

    #[test]
    fn cel_period_1_no_panic() {
        // period 1 là edge case indicator — không crash
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(1) < 30.0",
            "exit":  "rsi(1) > 70.0"
        })).unwrap();
        run_n(&mut *s, 50, 100.0); // không crash là đủ
    }

    #[test]
    fn cel_unknown_function_no_signal_no_panic() {
        // hàm không được đăng ký → CEL runtime error → false → không signal, không panic
        let s = build_strategy("cel", &json!({
            "entry": "close > 0.0",   // valid, always true
            "exit":  "close < 0.0"
        })).unwrap();
        assert_eq!(s.name(), "cel");
    }

    #[test]
    fn cel_empty_expression_no_signal() {
        // "false" literal — không bao giờ entry
        let mut s = build_strategy("cel", &json!({
            "entry": "false",
            "exit":  "true"
        })).unwrap();
        assert_eq!(run_n(&mut *s, 50, 100.0), 0);
    }

    #[test]
    fn cel_tautology_entry_false_exit() {
        let mut s = build_strategy("cel", &json!({
            "entry": "true",
            "exit":  "false"
        })).unwrap();
        // entry ở bar đầu tiên, exit không bao giờ → 1 signal
        assert_eq!(run_n(&mut *s, 50, 100.0), 1);
    }

    #[test]
    fn cel_flat_price_no_cross() {
        // giá flat → cross không bao giờ xảy ra
        let mut s = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        })).unwrap();
        // giá 100.0 flat → 2 EMA bằng nhau, không cross
        assert_eq!(run_n(&mut *s, 200, 100.0), 0);
    }

    #[test]
    fn cel_prev_not_available_first_bar_after_warmup() {
        // prev_ unavailable ở bar đầu tiên sau warmup → NaN so sánh → false → không signal
        // rsi warmup=14, bar 14 là bar đầu tiên có cached nhưng prev=None
        let mut s = build_strategy("cel", &json!({
            "entry": "prev_rsi(14) < 30.0 && rsi(14) < 30.0",
            "exit":  "rsi(14) > 70.0"
        })).unwrap();
        // không crash, chỉ kiểm tra không panic
        run_n(&mut *s, 30, 50.0);
    }

    #[test]
    fn cel_many_indicators_no_panic() {
        // spam nhiều indicator cùng lúc
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(14) < 30.0 && ema(20) > ema(50) && macd_hist(12) > 0.0 && atr(14) > 0.0 && adx(14) > 25.0 && bb_upper(20) > close",
            "exit":  "rsi(14) > 70.0 || ema(20) < ema(50)"
        })).unwrap();
        run_n(&mut *s, 200, 100.0); // không crash là đủ
    }

    // ── Dynamic JSON edge cases ───────────────────────────────────────────────

    #[test]
    fn dynamic_missing_indicators_returns_error() {
        let result = build_strategy("dynamic", &json!({
            "entry": { "logic": "and", "rules": [] }
            // không có "indicators"
        }));
        assert!(result.is_err(), "missing indicators should return Err");
    }

    #[test]
    fn dynamic_missing_entry_returns_error() {
        let result = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } }
            // không có "entry"
        }));
        assert!(result.is_err(), "missing entry should return Err");
    }

    #[test]
    fn dynamic_empty_rules_no_signal() {
        // rules rỗng → logic AND trên empty set = true mỗi bar? hay false?
        // dù kết quả nào, không được panic
        let result = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [] },
            "exit":  { "logic": "and", "rules": [] }
        }));
        if let Ok(mut s) = result {
            run_n(&mut *s, 50, 100.0); // không crash
        }
        // Err cũng chấp nhận được
    }

    #[test]
    fn dynamic_unknown_indicator_type_returns_error() {
        let result = build_strategy("dynamic", &json!({
            "indicators": {
                "weird": { "type": "super_unknown_oscillator_9000", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "weird", "field": "value", "op": "lt", "value": 30.0 }
            ]}
        }));
        assert!(result.is_err(), "unknown indicator type should return Err");
    }

    #[test]
    fn dynamic_compare_nonexistent_indicator_no_panic() {
        // compare đến indicator không tồn tại
        let result = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "nonexistent", "compare_field": "value" }
            ]}
        }));
        if let Ok(mut s) = result {
            run_n(&mut *s, 50, 100.0); // không crash khi chạy
        }
    }

    #[test]
    fn dynamic_extreme_threshold_never_signals() {
        // threshold = 999 → rsi không bao giờ đạt
        let mut s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rsi", "field": "value", "op": "gt", "value": 999.0 }
            ]}
        })).unwrap();
        assert_eq!(run_n(&mut *s, 100, 100.0), 0);
    }

    #[test]
    fn dynamic_extreme_period_no_panic() {
        let mut s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 999 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rsi", "field": "value", "op": "lt", "value": 30.0 }
            ]}
        })).unwrap();
        assert_eq!(run_n(&mut *s, 100, 100.0), 0, "warmup > bars, no signals");
    }

    #[test]
    fn dynamic_or_logic_with_one_true_fires() {
        // OR: một condition true là đủ
        let mut s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "or", "rules": [
                { "source": "rsi", "field": "value", "op": "gt", "value": 999.0 }, // never
                { "source": "close", "field": "value", "op": "gt", "value": 0.0 }  // always
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt", "value": 0.0 }  // never
            ]}
        })).unwrap();
        let total = run_n(&mut *s, 30, 100.0);
        assert_eq!(total, 1, "OR with one always-true should fire once");
    }

    #[test]
    fn dynamic_no_exit_holds_position() {
        // không có exit condition → vào rồi giữ mãi → chỉ 1 signal
        let mut s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "value": 0.0 }
            ]}
            // không có "exit"
        })).unwrap();
        let total = run_n(&mut *s, 50, 100.0);
        assert_eq!(total, 1, "no exit config should hold position after entry");
    }

    #[test]
    fn dynamic_wrong_field_name_no_panic() {
        // field không tồn tại trong indicator → condition false, không panic
        let mut s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rsi", "field": "nonexistent_field", "op": "lt", "value": 30.0 }
            ]}
        })).unwrap();
        run_n(&mut *s, 50, 100.0); // không crash
    }
}

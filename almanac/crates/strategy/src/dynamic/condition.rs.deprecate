use std::collections::HashMap;

use anyhow::{bail, Result};
use serde_json::Value;

#[derive(Debug, Clone, PartialEq)]
pub enum ConditionOp {
    Lt,
    Lte,
    Gt,
    Gte,
    CrossAbove, // current > threshold AND previous <= threshold
    CrossBelow, // current < threshold AND previous >= threshold
    Eq,
}

impl ConditionOp {
    fn from_str(s: &str) -> Result<Self> {
        match s {
            "lt" => Ok(Self::Lt),
            "lte" => Ok(Self::Lte),
            "gt" => Ok(Self::Gt),
            "gte" => Ok(Self::Gte),
            "cross_above" => Ok(Self::CrossAbove),
            "cross_below" => Ok(Self::CrossBelow),
            "eq" => Ok(Self::Eq),
            other => bail!("unknown condition op: '{other}'"),
        }
    }
}

/// Right-hand side of a condition: a literal scalar or another indicator field.
#[derive(Debug, Clone)]
pub enum Rhs {
    Scalar(f64),
    /// `{ "compare": "ema200" }` — compare to the `"value"` field of that indicator.
    IndicatorField { source: String, field: String },
}

/// A single comparison rule.
///
/// ```json
/// { "source": "rsi14", "field": "value", "op": "lt", "value": 30 }
/// { "source": "close", "field": "value", "op": "gt", "compare": "ema200" }
/// { "source": "macd",  "field": "histogram", "op": "cross_above", "value": 0 }
/// ```
#[derive(Debug, Clone)]
pub struct Condition {
    /// Named indicator (or special: "close", "open", "high", "low", "volume").
    pub source: String,
    /// Field within that indicator's output map (e.g. "value", "histogram").
    pub field: String,
    pub op: ConditionOp,
    pub rhs: Rhs,
}

impl Condition {
    pub fn from_value(v: &Value) -> Result<Self> {
        let source = v
            .get("source")
            .and_then(Value::as_str)
            .ok_or_else(|| anyhow::anyhow!("condition missing 'source'"))?
            .to_string();
        let field = v
            .get("field")
            .and_then(Value::as_str)
            .unwrap_or("value")
            .to_string();
        let op_str = v
            .get("op")
            .and_then(Value::as_str)
            .ok_or_else(|| anyhow::anyhow!("condition missing 'op'"))?;
        let op = ConditionOp::from_str(op_str)?;

        let rhs = if let Some(scalar) = v.get("value").and_then(Value::as_f64) {
            Rhs::Scalar(scalar)
        } else if let Some(compare_src) = v.get("compare").and_then(Value::as_str) {
            let compare_field = v
                .get("compare_field")
                .and_then(Value::as_str)
                .unwrap_or("value")
                .to_string();
            Rhs::IndicatorField {
                source: compare_src.to_string(),
                field: compare_field,
            }
        } else {
            bail!("condition must have 'value' (scalar) or 'compare' (indicator name)");
        };

        Ok(Self { source, field, op, rhs })
    }

    /// Evaluate this condition against the current snapshot of all indicator values.
    ///
    /// `current` and `previous` are maps from indicator_name → field_map.
    /// Special sources "close", "open", "high", "low", "volume" read from `bar_fields`.
    pub fn evaluate(
        &self,
        current: &HashMap<String, HashMap<String, f64>>,
        previous: &HashMap<String, HashMap<String, f64>>,
        bar_fields: &HashMap<String, f64>,
    ) -> bool {
        let lhs_cur = self.resolve(&self.source, &self.field, current, bar_fields);
        let lhs_prev = self.resolve(&self.source, &self.field, previous, bar_fields);

        let rhs_cur = match &self.rhs {
            Rhs::Scalar(s) => *s,
            Rhs::IndicatorField { source, field } => {
                self.resolve(source, field, current, bar_fields)
            }
        };
        let rhs_prev = match &self.rhs {
            Rhs::Scalar(s) => *s,
            Rhs::IndicatorField { source, field } => {
                self.resolve(source, field, previous, bar_fields)
            }
        };

        match self.op {
            ConditionOp::Lt => lhs_cur < rhs_cur,
            ConditionOp::Lte => lhs_cur <= rhs_cur,
            ConditionOp::Gt => lhs_cur > rhs_cur,
            ConditionOp::Gte => lhs_cur >= rhs_cur,
            ConditionOp::Eq => (lhs_cur - rhs_cur).abs() < 1e-10,
            ConditionOp::CrossAbove => lhs_cur > rhs_cur && lhs_prev <= rhs_prev,
            ConditionOp::CrossBelow => lhs_cur < rhs_cur && lhs_prev >= rhs_prev,
        }
    }

    fn resolve(
        &self,
        source: &str,
        field: &str,
        snapshot: &HashMap<String, HashMap<String, f64>>,
        bar_fields: &HashMap<String, f64>,
    ) -> f64 {
        match source {
            "close" | "open" | "high" | "low" | "volume" => {
                *bar_fields.get(source).unwrap_or(&0.0)
            }
            other => snapshot
                .get(other)
                .and_then(|m| m.get(field))
                .copied()
                .unwrap_or(0.0),
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub enum Logic {
    And,
    Or,
}

/// A group of conditions combined with AND or OR logic.
///
/// Supports flat rules and nested sub-groups:
///
/// ```json
/// {
///   "logic": "or",
///   "groups": [
///     { "logic": "and", "rules": [
///         { "source": "rsi14", "field": "value", "op": "lt", "value": 30 },
///         { "source": "close",  "op": "gt", "compare": "ema200" }
///     ]},
///     { "logic": "and", "rules": [
///         { "source": "macd", "field": "histogram", "op": "cross_above", "value": 0 },
///         { "source": "adx",  "field": "adx", "op": "gt", "value": 25 }
///     ]}
///   ]
/// }
/// ```
///
/// `rules` and `groups` can coexist and are evaluated together under the same logic.
#[derive(Debug, Clone)]
pub struct ConditionGroup {
    pub logic: Logic,
    /// Leaf conditions (direct comparisons).
    pub rules: Vec<Condition>,
    /// Nested sub-groups — each evaluates to a bool, then combined with siblings under `logic`.
    pub groups: Vec<ConditionGroup>,
}

impl ConditionGroup {
    pub fn from_value(v: &Value) -> Result<Self> {
        let logic_str = v.get("logic").and_then(Value::as_str).unwrap_or("and");
        let logic = match logic_str {
            "and" => Logic::And,
            "or"  => Logic::Or,
            other => bail!("unknown logic: '{other}'"),
        };

        // Flat rules (optional when using nested groups)
        let rules = if let Some(arr) = v.get("rules").and_then(Value::as_array) {
            arr.iter().map(Condition::from_value).collect::<Result<Vec<_>>>()?
        } else {
            vec![]
        };

        // Nested sub-groups (optional)
        let groups = if let Some(arr) = v.get("groups").and_then(Value::as_array) {
            arr.iter().map(Self::from_value).collect::<Result<Vec<_>>>()?
        } else {
            vec![]
        };

        if rules.is_empty() && groups.is_empty() {
            bail!("condition group must have at least one 'rules' item or 'groups' entry");
        }

        Ok(Self { logic, rules, groups })
    }

    pub fn evaluate(
        &self,
        current:    &HashMap<String, HashMap<String, f64>>,
        previous:   &HashMap<String, HashMap<String, f64>>,
        bar_fields: &HashMap<String, f64>,
    ) -> bool {
        // Collect bool results from leaf rules + nested groups
        let rule_results  = self.rules.iter().map(|r| r.evaluate(current, previous, bar_fields));
        let group_results = self.groups.iter().map(|g| g.evaluate(current, previous, bar_fields));
        let all: Vec<bool> = rule_results.chain(group_results).collect();

        if all.is_empty() { return false; }

        match self.logic {
            Logic::And => all.iter().all(|&b| b),
            Logic::Or  => all.iter().any(|&b| b),
        }
    }
}

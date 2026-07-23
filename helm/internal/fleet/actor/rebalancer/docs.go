// Package rebalancer is where a joint, multi-asset portfolio strategy would
// live — the one shape none of helm's Hands can do today, because every Hand
// decides and sizes alone. Point ten signal-following Hands at the same book
// and you get ten independent opinions, not a portfolio.
//
// The model: QuantConnect Lean's Algorithm Framework. Four stages, no shortcuts.
//
//	Alpha                  → per-asset signal. Already exists: core/strategy.Strategy.
//	Portfolio Construction → the one piece actually missing. Solve a joint
//	                         target-weight vector across the WHOLE book, not
//	                         one symbol at a time. Even equal-weight counts,
//	                         day one — the shape matters more than the math.
//	Risk Management        → already exists: core/risk.
//	Execution              → already exists: ProcessTrade → Tactics → the order layer.
//
// Everything downstream of Portfolio Construction is a solved problem. Don't
// touch it. The only genuinely new machinery here is the solve.
//
// What this is NOT: a HandType. Not a Strategy variant wired into
// BuildHandComponents. A per-hand, per-symbol interface can't host a joint
// solve — Hand's entire contract is "decide alone," so bolting a portfolio
// optimizer onto it doesn't just misplace the code, it makes the joint solve
// structurally impossible. This has to sit above Hand — likely at or above
// HelmRuntime — and come out the other side as ordinary per-symbol trade
// intents that flow through the existing pipeline unchanged once the target
// weights are decided.
//
// Also owed, not yet built: symbol reservation. A rebalancer holding BTCUSDT
// for the book needs HelmRuntime to actually stop some unrelated
// signal-follower Hand from trading it out from under the rebalance mid-flight.
// Nothing enforces that today — same shape of gap as the cross-hand leverage
// race already fixed once (see helm_actor.go's leverageSet). This one's still open.
//
// STATUS: direction, not code. Nothing here is wired to HandType,
// BuildHandComponents, or HelmRuntime. Don't build against this package
// assuming any of the above exists yet.
package rebalancer

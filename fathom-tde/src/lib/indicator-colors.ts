// Shared cycling palette for script-declared indicators — used by both EditorPanel's
// IndicatorsRail (declaration chips) and ChartPanel's overlay line series, so a given
// indicator's chip color and its chart line color match as long as both iterate declarations in
// the same (script) order. Not theme-specific: indicator count/identity is script-defined, so a
// fixed per-theme palette can't cover it — picked to read reasonably on both chart backgrounds.
export const INDICATOR_COLORS = ['#f59e0b', '#8b5cf6', '#06b6d4', '#ec4899', '#84cc16', '#f43f5e', '#3b82f6', '#eab308']

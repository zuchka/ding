# Compound Alert Conditions — Design Spec

**Date:** 2026-03-28
**Status:** Draft

---

## Context

DING currently evaluates alert conditions using two fixed regex patterns: a simple event-per-event form (`value > 95`) and a windowed aggregation form (`avg(value) over 5m > 80`). Adding any new syntax requires a new regex and hand-wired parsing logic.

Competitors support compound metric evaluation — conditions joined with `AND`/`OR`. To support this without creating a DSL or requiring users to learn a query language, we replace the regex-based parser with a minimal recursive descent parser and introduce a `ConditionExpr` AST. Compound syntax reads like plain English; no new concepts are introduced.

---

## Goals

- Support `AND` and `OR` in condition strings
- Keep existing single-condition syntax unchanged
- No external dependencies
- No parentheses / no DSL — just composable readable conditions
- Standard operator precedence: `AND` binds tighter than `OR`

---

## Non-Goals

- Parenthesized grouping (future extension point)
- Arithmetic expressions (`value * 1.1 > 95`)
- Cross-metric correlations in a single condition string
- Custom aggregation functions

---

## Syntax

Existing syntax is unchanged for single conditions:

```yaml
condition: value > 95
condition: avg(value) over 5m > 80
```

New compound forms:

```yaml
condition: value > 90 AND value < 100
condition: avg(value) over 5m > 80 OR max(value) over 1m > 95
condition: value > 90 AND avg(value) over 5m > 80
```

`AND` binds tighter than `OR`. Keywords must be uppercase and space-delimited (` AND `, ` OR `). Lowercase `and`/`or` will not be recognized as operators and will produce a clear parse error rather than silently failing.

---

## Architecture

### AST (`condition.go`)

```
ConditionExpr (interface)
├── leafExpr        — wraps existing Condition, has integer ID for windowed leaves
└── compoundExpr    — op ("AND"/"OR"), left ConditionExpr, right ConditionExpr
```

**`leafExpr`** — a single parsed condition. Windowed leaves are assigned a unique integer ID at parse time (sequential, zero-based per rule). Event-per-event leaves use `id = -1`.

**`compoundExpr`** — an AND/OR node. Eval short-circuits on the logical operator: `AND` returns false early on left=false, `OR` returns true early on left=true.

**`evalContext`** — passed to `Eval()` at runtime:
```go
type evalContext struct {
    Value      float64
    Aggregates map[int]float64  // leafID -> aggregate value
    Available  map[int]bool     // leafID -> buffer has entries
}
```

**`CollectWindowedLeaves() []windowedLeaf`** — a method on `ConditionExpr` that performs a depth-first tree walk and returns all windowed leaf nodes (with their ID, func, and window). Called once per `Process()` invocation to determine which ring buffers to populate.

### Tokenizer (`condition.go`)

Scans the condition string left-to-right, splitting on ` AND ` and ` OR ` (space-bounded). Emits a flat token slice:
- `TokAtom` — a raw condition string (passed to existing `ParseCondition`)
- `TokAND`
- `TokOR`

A leading/trailing `AND`/`OR` with a missing operand is a tokenization error.

### Parser (`condition.go`)

Recursive descent, two levels:

```
expr      ::= and_expr (OR and_expr)*
and_expr  ::= atom (AND atom)*
atom      ::= ParseCondition(token)
```

Public entry point: `ParseConditionExpr(s string) (ConditionExpr, error)`

`ParseCondition` is retained as an internal building block for parsing individual atoms. Errors from atom parsing are wrapped with context: `invalid atom in condition "value over 95": unrecognized condition syntax`.

### Engine Changes (`engine.go`)

`parsedRule.cond Condition` → `parsedRule.expr ConditionExpr`

`NewEngine` calls `ParseConditionExpr` instead of `ParseCondition`.

**`Process()` new flow:**

1. Walk `rule.expr` via `CollectWindowedLeaves()` to get all windowed leaves
2. **For every windowed leaf** (regardless of compound logic): get/create ring buffer keyed `ruleName:leafID:labelSetKey`, add `event.Value`. Buffer population is unconditional — it happens before evaluation so that buffers stay current even when short-circuit logic would skip a leaf's evaluation.
3. Check `HasEntries` for each windowed leaf; populate `evalContext.Available[id]`
4. Compute aggregate for each windowed leaf; populate `evalContext.Aggregates[id]`
5. Build `evalContext{Value: event.Value, Aggregates: ..., Available: ...}`
6. Call `rule.expr.Eval(ctx)` — if false, skip alert
7. Cooldown check and alert generation unchanged

**Buffer key change:** `ruleName:labelSetKey` → `ruleName:leafID:labelSetKey`

Single-condition rules get `leafID = 0` automatically, so existing behavior is preserved.

### Alert Struct & Message Templates

The `Alert` struct retains its existing flat aggregate fields (`Avg`, `Max`, `Min`, `Count`, `Sum`). For backward compatibility:

- **Single windowed condition:** populated as today (leafID 0 values)
- **Compound conditions with multiple windowed leaves:** fields are zero / not populated. Users writing compound windowed conditions should not rely on `{{ .avg }}` etc. in their message templates. This is documented in the config reference.

No changes to the `Alert` struct shape or `renderMessage`.

---

## Snapshot Compatibility

The buffer key format changes from `ruleName:labelSetKey` to `ruleName:leafID:labelSetKey`. Existing snapshot files (version 1) store keys in the old format. On upgrade:

- Old buffer keys in the snapshot will not match any new key lookups and will be silently ignored during `RestoreEngine` (the restored buffers are loaded into `e.buffers` under old keys but never accessed)
- Windowed rules will go through a one-time warm-up period after restart
- Cooldown state is keyed differently (by rule name + label set, not buffer key) and is **unaffected**

The snapshot `Version` field remains `1`. No migration code is required; the silent-ignore behavior is acceptable for a one-time restart cost.

---

## Files Changed

| File | Change |
|------|--------|
| `internal/evaluator/condition.go` | Add `ConditionExpr`, `leafExpr`, `compoundExpr`, `evalContext`, `windowedLeaf`, `CollectWindowedLeaves()`, tokenizer, recursive descent parser, `ParseConditionExpr` |
| `internal/evaluator/engine.go` | Replace `cond Condition` with `expr ConditionExpr`; refactor `Process()` buffer management and eval call |
| `internal/evaluator/evaluator_test.go` | Add tests for compound parsing and compound evaluation |

---

## Error Handling

- `ParseConditionExpr` returns a descriptive error if any atom fails to parse, wrapping with context
- Lowercase `and`/`or` produces: `unrecognized condition syntax: did you mean "AND"/"OR"?`
- `AND`/`OR` with a missing operand is a parse error: `unexpected end of condition after "AND"`

---

## Testing

**Parsing:**
- `value > 90 AND value < 100` → `compoundExpr{AND, leaf, leaf}`
- `a AND b OR c` → `compoundExpr{OR, compoundExpr{AND, leaf, leaf}, leaf}` (AND-first precedence)
- `a OR b AND c` → `compoundExpr{OR, leaf, compoundExpr{AND, leaf, leaf}}` (symmetric case)
- Invalid atom in compound → error with atom context
- Lowercase `and`/`or` → error with hint

**Evaluation:**
- AND: both sides true → fires; either side false → no alert
- OR: either side true → fires; both false → no alert
- Windowed leaf inside AND: buffer is always populated even when left side is false (pre-pass before eval)
- Existing event-per-event and windowed single-condition tests pass unchanged

**Integration:**
- `NewEngine` validates compound conditions at startup (same as today for single conditions)
- `ding validate` catches malformed compound conditions via config validation

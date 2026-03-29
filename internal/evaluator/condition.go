package evaluator

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reEventCond    = regexp.MustCompile(`^value\s*(>|>=|<|<=|==|!=)\s*(\d+(?:\.\d+)?)$`)
	reWindowedCond = regexp.MustCompile(`^(avg|max|min|count|sum)\(value\)\s+over\s+(\d+[smh])\s*(>|>=|<|<=|==|!=)\s*(\d+(?:\.\d+)?)$`)
)

// Condition is a parsed alert condition.
type Condition struct {
	Windowed bool
	Op       string
	Literal  float64
	Func     string        // windowed only: avg|max|min|count|sum
	Window   time.Duration // windowed only
}

// ParseCondition parses a condition string from ding.yaml.
func ParseCondition(s string) (Condition, error) {
	if m := reEventCond.FindStringSubmatch(s); m != nil {
		lit, _ := strconv.ParseFloat(m[2], 64)
		return Condition{Windowed: false, Op: m[1], Literal: lit}, nil
	}
	if m := reWindowedCond.FindStringSubmatch(s); m != nil {
		fn := m[1]
		dur, err := time.ParseDuration(m[2])
		if err != nil {
			return Condition{}, fmt.Errorf("invalid duration in condition %q: %w", s, err)
		}
		lit, _ := strconv.ParseFloat(m[4], 64)
		return Condition{Windowed: true, Func: fn, Window: dur, Op: m[3], Literal: lit}, nil
	}
	return Condition{}, fmt.Errorf("unrecognized condition syntax: %q", s)
}

// applyOp evaluates "actual OP literal".
func applyOp(actual float64, op string, literal float64) bool {
	switch op {
	case ">":
		return actual > literal
	case ">=":
		return actual >= literal
	case "<":
		return actual < literal
	case "<=":
		return actual <= literal
	case "==":
		return actual == literal
	case "!=":
		return actual != literal
	}
	return false
}

// ConditionExpr is a node in the parsed condition expression tree.
// Implementations are leafExpr (single condition) and compoundExpr (AND/OR).
type ConditionExpr interface {
	// eval evaluates the expression against the provided context.
	eval(ctx evalContext) bool
	// collectWindowedLeaves returns all windowed leaf nodes in the tree.
	// Called once per Process() invocation to drive buffer management.
	collectWindowedLeaves() []windowedLeaf
}

// evalContext holds the runtime values needed to evaluate a ConditionExpr.
// Aggregates and Available are keyed by the windowedLeaf.ID assigned at parse time.
// The ID matches the integer used in the ring buffer key: "ruleName:ID:labelSetKey".
type evalContext struct {
	Value      float64
	Aggregates map[int]float64 // leafID -> computed aggregate value
	Available  map[int]bool    // leafID -> ring buffer has entries in window
}

// windowedLeaf describes a windowed sub-condition for ring buffer management.
// ID is the sequential integer assigned at parse time (zero-based per rule) and
// is the same integer used as the middle segment of the buffer key.
type windowedLeaf struct {
	ID     int
	Func   string
	Window time.Duration
}

// leafExpr is a leaf node wrapping a single parsed Condition.
// For windowed conditions id is the sequential index (0, 1, 2…); for event-per-event id is -1.
type leafExpr struct {
	Condition
	id int
}

func (l *leafExpr) eval(ctx evalContext) bool {
	if l.Windowed {
		if !ctx.Available[l.id] {
			return false
		}
		return applyOp(ctx.Aggregates[l.id], l.Op, l.Literal)
	}
	return applyOp(ctx.Value, l.Op, l.Literal)
}

func (l *leafExpr) collectWindowedLeaves() []windowedLeaf {
	if l.Windowed {
		return []windowedLeaf{{ID: l.id, Func: l.Func, Window: l.Window}}
	}
	return nil
}

// compoundExpr is an AND/OR node with two sub-expressions.
// AND short-circuits on left=false; OR short-circuits on left=true.
type compoundExpr struct {
	op    string // "AND" or "OR"
	left  ConditionExpr
	right ConditionExpr
}

func (c *compoundExpr) eval(ctx evalContext) bool {
	switch c.op {
	case "AND":
		return c.left.eval(ctx) && c.right.eval(ctx)
	case "OR":
		return c.left.eval(ctx) || c.right.eval(ctx)
	}
	return false
}

func (c *compoundExpr) collectWindowedLeaves() []windowedLeaf {
	return append(c.left.collectWindowedLeaves(), c.right.collectWindowedLeaves()...)
}

type tokenKind int

const (
	tokAtom tokenKind = iota
	tokAND
	tokOR
)

type token struct {
	kind tokenKind
	val  string // non-empty for tokAtom
}

// tokenize splits s on " AND " and " OR " delimiters (space-bounded, uppercase only).
// Returns a flat token slice alternating atoms and operators.
func tokenize(s string) ([]token, error) {
	var tokens []token
	rest := s
	for len(rest) > 0 {
		andIdx := strings.Index(rest, " AND ")
		orIdx := strings.Index(rest, " OR ")

		switch {
		case andIdx == -1 && orIdx == -1:
			atom := strings.TrimSpace(rest)
			if atom == "" {
				return nil, fmt.Errorf("unexpected end of condition")
			}
			tokens = append(tokens, token{kind: tokAtom, val: atom})
			rest = ""
		case andIdx != -1 && (orIdx == -1 || andIdx < orIdx):
			atom := strings.TrimSpace(rest[:andIdx])
			if atom == "" {
				return nil, fmt.Errorf("operator AND without left operand")
			}
			tokens = append(tokens, token{kind: tokAtom, val: atom})
			tokens = append(tokens, token{kind: tokAND})
			rest = rest[andIdx+5:] // len(" AND ") == 5
		default:
			atom := strings.TrimSpace(rest[:orIdx])
			if atom == "" {
				return nil, fmt.Errorf("operator OR without left operand")
			}
			tokens = append(tokens, token{kind: tokAtom, val: atom})
			tokens = append(tokens, token{kind: tokOR})
			rest = rest[orIdx+4:] // len(" OR ") == 4
		}
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty condition")
	}
	if tokens[len(tokens)-1].kind != tokAtom {
		return nil, fmt.Errorf("unexpected end of condition after operator")
	}
	return tokens, nil
}

type condParser struct {
	tokens []token
	pos    int
	nextID int // sequential ID counter for windowed leaves (zero-based per rule)
}

func (p *condParser) peek() (token, bool) {
	if p.pos >= len(p.tokens) {
		return token{}, false
	}
	return p.tokens[p.pos], true
}

func (p *condParser) consume() token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

// parseExpr handles OR (lowest precedence): expr ::= and_expr (OR and_expr)*
func (p *condParser) parseExpr() (ConditionExpr, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != tokOR {
			break
		}
		p.consume()
		right, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		left = &compoundExpr{op: "OR", left: left, right: right}
	}
	return left, nil
}

// parseAndExpr handles AND (higher precedence): and_expr ::= atom (AND atom)*
func (p *condParser) parseAndExpr() (ConditionExpr, error) {
	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	for {
		tok, ok := p.peek()
		if !ok || tok.kind != tokAND {
			break
		}
		p.consume()
		right, err := p.parseAtom()
		if err != nil {
			return nil, err
		}
		left = &compoundExpr{op: "AND", left: left, right: right}
	}
	return left, nil
}

func (p *condParser) parseAtom() (ConditionExpr, error) {
	tok, ok := p.peek()
	if !ok {
		return nil, fmt.Errorf("unexpected end of condition")
	}
	if tok.kind != tokAtom {
		return nil, fmt.Errorf("unexpected operator without operand")
	}
	p.consume()
	cond, err := ParseCondition(tok.val)
	if err != nil {
		lower := strings.ToLower(tok.val)
		if strings.Contains(lower, " and ") || strings.Contains(lower, " or ") {
			return nil, fmt.Errorf("invalid condition %q: did you mean AND/OR? keywords must be uppercase", tok.val)
		}
		return nil, fmt.Errorf("invalid atom %q: %w", tok.val, err)
	}
	leaf := &leafExpr{Condition: cond, id: -1}
	if cond.Windowed {
		leaf.id = p.nextID
		p.nextID++
	}
	return leaf, nil
}

// ParseConditionExpr parses a condition string (which may contain AND/OR) into
// a ConditionExpr tree. Single conditions produce a leafExpr; compound conditions
// produce a compoundExpr tree with AND binding tighter than OR.
func ParseConditionExpr(s string) (ConditionExpr, error) {
	tokens, err := tokenize(s)
	if err != nil {
		return nil, fmt.Errorf("condition %q: %w", s, err)
	}
	p := &condParser{tokens: tokens}
	expr, err := p.parseExpr()
	if err != nil {
		return nil, fmt.Errorf("condition %q: %w", s, err)
	}
	return expr, nil
}

package evaluator

import (
	"fmt"
	"regexp"
	"strconv"
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

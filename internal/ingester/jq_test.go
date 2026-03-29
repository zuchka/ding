package ingester_test

import (
	"strings"
	"testing"

	"github.com/zuchka/ding/internal/ingester"
)

func TestCompileJQ_Valid(t *testing.T) {
	_, err := ingester.CompileJQ(`.x`)
	if err != nil {
		t.Fatalf("expected no error for valid expression, got %v", err)
	}
}

func TestCompileJQ_Invalid(t *testing.T) {
	_, err := ingester.CompileJQ(`not valid jq |||`)
	if err == nil {
		t.Fatal("expected error for invalid JQ expression, got nil")
	}
}

func TestRunJQ_SingleObject(t *testing.T) {
	code, _ := ingester.CompileJQ(`{metric: .name, value: .reading}`)
	events, err := ingester.RunJQ(code, []byte(`{"name":"cpu_usage","reading":95.0}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Metric != "cpu_usage" {
		t.Errorf("expected metric cpu_usage, got %s", events[0].Metric)
	}
	if events[0].Value != 95.0 {
		t.Errorf("expected value 95.0, got %f", events[0].Value)
	}
}

func TestRunJQ_ArrayOutput(t *testing.T) {
	code, _ := ingester.CompileJQ(`.events[]`)
	input := `{"events":[{"metric":"cpu_usage","value":80},{"metric":"mem_usage","value":60}]}`
	events, err := ingester.RunJQ(code, []byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Metric != "cpu_usage" {
		t.Errorf("expected first metric cpu_usage, got %s", events[0].Metric)
	}
	if events[1].Metric != "mem_usage" {
		t.Errorf("expected second metric mem_usage, got %s", events[1].Metric)
	}
}

func TestRunJQ_ExtraFieldsBecomeLabels(t *testing.T) {
	code, _ := ingester.CompileJQ(`{metric: .name, value: .v, host: .host}`)
	events, err := ingester.RunJQ(code, []byte(`{"name":"cpu_usage","v":50,"host":"web-01"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if events[0].Labels["host"] != "web-01" {
		t.Errorf("expected label host=web-01, got %q", events[0].Labels["host"])
	}
}

func TestRunJQ_NullOutput(t *testing.T) {
	code, _ := ingester.CompileJQ(`null`)
	_, err := ingester.RunJQ(code, []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for null output, got nil")
	}
}

func TestRunJQ_EmptyArrayOutput(t *testing.T) {
	code, _ := ingester.CompileJQ(`.events[]`)
	_, err := ingester.RunJQ(code, []byte(`{"events":[]}`))
	if err == nil {
		t.Fatal("expected error for empty array output, got nil")
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Errorf("expected 'no output' in error, got: %v", err)
	}
}

func TestRunJQ_NonObjectOutput_String(t *testing.T) {
	code, _ := ingester.CompileJQ(`.name`)
	_, err := ingester.RunJQ(code, []byte(`{"name":"cpu_usage"}`))
	if err == nil {
		t.Fatal("expected error for non-object output, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected output type") {
		t.Errorf("expected 'unexpected output type' in error, got: %v", err)
	}
}

func TestRunJQ_NonObjectOutput_Number(t *testing.T) {
	code, _ := ingester.CompileJQ(`.v`)
	_, err := ingester.RunJQ(code, []byte(`{"v":42}`))
	if err == nil {
		t.Fatal("expected error for number output, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected output type") {
		t.Errorf("expected 'unexpected output type' in error, got: %v", err)
	}
}

func TestRunJQ_MissingMetric(t *testing.T) {
	code, _ := ingester.CompileJQ(`{value: .v}`)
	_, err := ingester.RunJQ(code, []byte(`{"v":42}`))
	if err == nil {
		t.Fatal("expected error for missing metric field, got nil")
	}
}

func TestRunJQ_MissingValue(t *testing.T) {
	code, _ := ingester.CompileJQ(`{metric: .m}`)
	_, err := ingester.RunJQ(code, []byte(`{"m":"cpu_usage"}`))
	if err == nil {
		t.Fatal("expected error for missing value field, got nil")
	}
}

func TestRunJQ_InvalidInputJSON(t *testing.T) {
	code, _ := ingester.CompileJQ(`.x`)
	_, err := ingester.RunJQ(code, []byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON input, got nil")
	}
}

func TestRunJQ_RuntimeError(t *testing.T) {
	// .foo.bar on {"foo": 42} causes a JQ runtime type error
	// because .foo returns a number, and you can't index a number with .bar
	code, _ := ingester.CompileJQ(`.foo.bar`)
	_, err := ingester.RunJQ(code, []byte(`{"foo": 42}`))
	if err == nil {
		t.Fatal("expected error for JQ runtime type error, got nil")
	}
	if !strings.Contains(err.Error(), "jq runtime error") {
		t.Errorf("expected 'jq runtime error' in message, got: %v", err)
	}
}

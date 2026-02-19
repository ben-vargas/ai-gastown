package cmd

import (
	"strings"
	"testing"
	"time"
)

func TestFormatQueueMetadata_AllFields(t *testing.T) {
	m := &QueueMetadata{
		TargetRig:        "myrig",
		Formula:          "mol-polecat-work",
		Args:             "implement feature X",
		Vars:             "a=1\nb=2",
		EnqueuedAt:       "2026-01-15T10:00:00Z",
		Merge:            "direct",
		Convoy:           "hq-cv-test",
		BaseBranch:       "develop",
		NoMerge:          true,
		Account:          "acme",
		Agent:            "gemini",
		HookRawBead:      true,
		Owned:            true,
		DispatchFailures: 2,
		LastFailure:      "sling failed: timeout",
	}

	result := FormatQueueMetadata(m)

	// Must start with versioned delimiter
	if !strings.HasPrefix(result, "---gt:queue:v1---") {
		t.Fatalf("expected result to start with delimiter, got:\n%s", result)
	}

	expected := []string{
		"target_rig: myrig",
		"formula: mol-polecat-work",
		"args: implement feature X",
		"var: a=1",
		"var: b=2",
		"enqueued_at: 2026-01-15T10:00:00Z",
		"merge: direct",
		"convoy: hq-cv-test",
		"base_branch: develop",
		"no_merge: true",
		"account: acme",
		"agent: gemini",
		"hook_raw_bead: true",
		"owned: true",
		"dispatch_failures: 2",
		"last_failure: sling failed: timeout",
	}
	for _, want := range expected {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q in output:\n%s", want, result)
		}
	}
}

func TestFormatQueueMetadata_MinimalFields(t *testing.T) {
	m := &QueueMetadata{
		TargetRig:  "prod",
		EnqueuedAt: "2026-01-15T10:00:00Z",
	}

	result := FormatQueueMetadata(m)

	if !strings.Contains(result, "target_rig: prod") {
		t.Errorf("missing target_rig in output:\n%s", result)
	}
	if !strings.Contains(result, "enqueued_at: 2026-01-15T10:00:00Z") {
		t.Errorf("missing enqueued_at in output:\n%s", result)
	}

	// Omitted fields should not appear
	for _, absent := range []string{"formula:", "args:", "var:", "merge:", "convoy:", "base_branch:", "no_merge:", "account:", "agent:", "hook_raw_bead:", "owned:", "dispatch_failures:", "last_failure:"} {
		if strings.Contains(result, absent) {
			t.Errorf("should not contain %q when field is empty:\n%s", absent, result)
		}
	}
}

func TestFormatQueueMetadata_BoolFields(t *testing.T) {
	// All bools true
	m := &QueueMetadata{
		TargetRig:   "rig1",
		EnqueuedAt:  "2026-01-15T10:00:00Z",
		NoMerge:     true,
		HookRawBead: true,
		Owned:       true,
	}
	result := FormatQueueMetadata(m)
	for _, want := range []string{"no_merge: true", "hook_raw_bead: true", "owned: true"} {
		if !strings.Contains(result, want) {
			t.Errorf("missing %q when bool is true:\n%s", want, result)
		}
	}

	// All bools false — should be absent
	m2 := &QueueMetadata{
		TargetRig:  "rig1",
		EnqueuedAt: "2026-01-15T10:00:00Z",
	}
	result2 := FormatQueueMetadata(m2)
	for _, absent := range []string{"no_merge:", "hook_raw_bead:", "owned:"} {
		if strings.Contains(result2, absent) {
			t.Errorf("should not contain %q when bool is false:\n%s", absent, result2)
		}
	}
}

func TestParseQueueMetadata_RoundTrip(t *testing.T) {
	original := &QueueMetadata{
		TargetRig:        "myrig",
		Formula:          "mol-polecat-work",
		Args:             "do the thing",
		Vars:             "x=1\ny=2",
		EnqueuedAt:       "2026-01-15T10:00:00Z",
		Merge:            "mr",
		Convoy:           "hq-cv-abc",
		BaseBranch:       "main",
		NoMerge:          true,
		Account:          "test-acct",
		Agent:            "codex",
		HookRawBead:      true,
		Owned:            true,
		DispatchFailures: 1,
		LastFailure:      "sling failed: rig not found",
	}

	formatted := FormatQueueMetadata(original)
	parsed := ParseQueueMetadata(formatted)

	if parsed == nil {
		t.Fatal("ParseQueueMetadata returned nil")
	}

	if parsed.TargetRig != original.TargetRig {
		t.Errorf("TargetRig: got %q, want %q", parsed.TargetRig, original.TargetRig)
	}
	if parsed.Formula != original.Formula {
		t.Errorf("Formula: got %q, want %q", parsed.Formula, original.Formula)
	}
	if parsed.Args != original.Args {
		t.Errorf("Args: got %q, want %q", parsed.Args, original.Args)
	}
	if parsed.Vars != original.Vars {
		t.Errorf("Vars: got %q, want %q", parsed.Vars, original.Vars)
	}
	if parsed.EnqueuedAt != original.EnqueuedAt {
		t.Errorf("EnqueuedAt: got %q, want %q", parsed.EnqueuedAt, original.EnqueuedAt)
	}
	if parsed.Merge != original.Merge {
		t.Errorf("Merge: got %q, want %q", parsed.Merge, original.Merge)
	}
	if parsed.Convoy != original.Convoy {
		t.Errorf("Convoy: got %q, want %q", parsed.Convoy, original.Convoy)
	}
	if parsed.BaseBranch != original.BaseBranch {
		t.Errorf("BaseBranch: got %q, want %q", parsed.BaseBranch, original.BaseBranch)
	}
	if parsed.NoMerge != original.NoMerge {
		t.Errorf("NoMerge: got %v, want %v", parsed.NoMerge, original.NoMerge)
	}
	if parsed.Account != original.Account {
		t.Errorf("Account: got %q, want %q", parsed.Account, original.Account)
	}
	if parsed.Agent != original.Agent {
		t.Errorf("Agent: got %q, want %q", parsed.Agent, original.Agent)
	}
	if parsed.HookRawBead != original.HookRawBead {
		t.Errorf("HookRawBead: got %v, want %v", parsed.HookRawBead, original.HookRawBead)
	}
	if parsed.Owned != original.Owned {
		t.Errorf("Owned: got %v, want %v", parsed.Owned, original.Owned)
	}
	if parsed.DispatchFailures != original.DispatchFailures {
		t.Errorf("DispatchFailures: got %d, want %d", parsed.DispatchFailures, original.DispatchFailures)
	}
	if parsed.LastFailure != original.LastFailure {
		t.Errorf("LastFailure: got %q, want %q", parsed.LastFailure, original.LastFailure)
	}
}

func TestParseQueueMetadata_NoDelimiter(t *testing.T) {
	result := ParseQueueMetadata("Just a regular description without queue metadata")
	if result != nil {
		t.Fatalf("expected nil for description without delimiter, got %+v", result)
	}
}

func TestParseQueueMetadata_WithPreamble(t *testing.T) {
	desc := "This is a task description.\nIt has multiple lines.\n---gt:queue:v1---\ntarget_rig: myrig\nformula: test-formula\nenqueued_at: 2026-01-15T10:00:00Z"

	parsed := ParseQueueMetadata(desc)
	if parsed == nil {
		t.Fatal("ParseQueueMetadata returned nil")
	}
	if parsed.TargetRig != "myrig" {
		t.Errorf("TargetRig: got %q, want %q", parsed.TargetRig, "myrig")
	}
	if parsed.Formula != "test-formula" {
		t.Errorf("Formula: got %q, want %q", parsed.Formula, "test-formula")
	}
	if parsed.EnqueuedAt != "2026-01-15T10:00:00Z" {
		t.Errorf("EnqueuedAt: got %q, want %q", parsed.EnqueuedAt, "2026-01-15T10:00:00Z")
	}
}

func TestParseQueueMetadata_IgnoresUnknownKeys(t *testing.T) {
	desc := "---gt:queue:v1---\ntarget_rig: rig1\nfuture_field: xyz\nenqueued_at: 2026-01-15T10:00:00Z\nanother_unknown: 42"

	parsed := ParseQueueMetadata(desc)
	if parsed == nil {
		t.Fatal("ParseQueueMetadata returned nil")
	}
	if parsed.TargetRig != "rig1" {
		t.Errorf("TargetRig: got %q, want %q", parsed.TargetRig, "rig1")
	}
	if parsed.EnqueuedAt != "2026-01-15T10:00:00Z" {
		t.Errorf("EnqueuedAt: got %q, want %q", parsed.EnqueuedAt, "2026-01-15T10:00:00Z")
	}
}

func TestParseQueueMetadata_StopsAtSecondDelimiter(t *testing.T) {
	desc := "---gt:queue:v1---\ntarget_rig: rig1\nenqueued_at: 2026-01-15T10:00:00Z\n---gt:queue:v1---\ntarget_rig: should-be-ignored"

	parsed := ParseQueueMetadata(desc)
	if parsed == nil {
		t.Fatal("ParseQueueMetadata returned nil")
	}
	if parsed.TargetRig != "rig1" {
		t.Errorf("TargetRig: got %q, want %q (should stop at second delimiter)", parsed.TargetRig, "rig1")
	}
}

func TestStripQueueMetadata_RemovesBlock(t *testing.T) {
	preamble := "Task description here"
	desc := preamble + "\n---gt:queue:v1---\ntarget_rig: rig1\nenqueued_at: 2026-01-15T10:00:00Z"

	stripped := StripQueueMetadata(desc)
	if stripped != preamble {
		t.Errorf("StripQueueMetadata: got %q, want %q", stripped, preamble)
	}
}

func TestStripQueueMetadata_NoMetadata(t *testing.T) {
	desc := "Just a regular description"
	stripped := StripQueueMetadata(desc)
	if stripped != desc {
		t.Errorf("StripQueueMetadata: got %q, want %q", stripped, desc)
	}
}

func TestNewQueueMetadata_SetsTimestamp(t *testing.T) {
	before := time.Now().UTC()
	m := NewQueueMetadata("test-rig")
	after := time.Now().UTC()

	if m.TargetRig != "test-rig" {
		t.Errorf("TargetRig: got %q, want %q", m.TargetRig, "test-rig")
	}

	ts, err := time.Parse(time.RFC3339, m.EnqueuedAt)
	if err != nil {
		t.Fatalf("EnqueuedAt is not valid RFC3339: %q, err: %v", m.EnqueuedAt, err)
	}

	if ts.Before(before.Truncate(time.Second)) || ts.After(after.Add(time.Second)) {
		t.Errorf("EnqueuedAt %v not between %v and %v", ts, before, after)
	}
}

func TestResolveFormula(t *testing.T) {
	tests := []struct {
		name        string
		explicit    string
		hookRawBead bool
		want        string
	}{
		{"default", "", false, "mol-polecat-work"},
		{"explicit formula", "my-custom", false, "my-custom"},
		{"hook-raw-bead suppresses default", "", true, ""},
		{"hook-raw-bead suppresses explicit", "my-custom", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveFormula(tt.explicit, tt.hookRawBead)
			if got != tt.want {
				t.Errorf("resolveFormula(%q, %v) = %q, want %q", tt.explicit, tt.hookRawBead, got, tt.want)
			}
		})
	}
}

func TestHasQueuedLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"present", []string{"other", "gt:queued", "more"}, true},
		{"absent", []string{"other", "gt:something"}, false},
		{"empty", nil, false},
		{"empty slice", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasQueuedLabel(tt.labels)
			if got != tt.want {
				t.Errorf("hasQueuedLabel(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

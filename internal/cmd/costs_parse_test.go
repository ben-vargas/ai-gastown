package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTranscriptLine creates a JSONL line for a transcript assistant message.
func writeTranscriptLine(msgID, model string, input, output, cacheRead, cacheCreate int) string {
	msg := TranscriptMessage{
		Type:      "assistant",
		SessionID: "test-session",
		Message: &TranscriptMessageBody{
			ID:    msgID,
			Model: model,
			Role:  "assistant",
			Usage: &TranscriptUsage{
				InputTokens:              input,
				OutputTokens:             output,
				CacheReadInputTokens:     cacheRead,
				CacheCreationInputTokens: cacheCreate,
			},
		},
	}
	data, _ := json.Marshal(msg)
	return string(data)
}

func writeTranscriptFile(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseTranscriptUsage_Deduplication(t *testing.T) {
	// Simulate streaming partials: same message.id appears multiple times.
	// Only the last entry per message.id should be counted.
	path := writeTranscriptFile(t,
		// Message 1: 3 streaming partials
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 5, 5000, 1000),
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 10, 5000, 1000),
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 50, 5000, 1000), // final
		// Message 2: 2 streaming partials
		writeTranscriptLine("msg_002", "claude-opus-4-6", 200, 8, 10000, 500),
		writeTranscriptLine("msg_002", "claude-opus-4-6", 200, 30, 10000, 500), // final
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("parseTranscriptUsage() error: %v", err)
	}

	tokens, ok := usage.ByModel["claude-opus-4-6"]
	if !ok {
		t.Fatal("expected model claude-opus-4-6 in results")
	}

	// Deduplicated: msg_001 final (100,50,5000,1000) + msg_002 final (200,30,10000,500)
	if tokens.InputTokens != 300 {
		t.Errorf("InputTokens = %d, want 300 (deduplicated)", tokens.InputTokens)
	}
	if tokens.OutputTokens != 80 {
		t.Errorf("OutputTokens = %d, want 80 (deduplicated)", tokens.OutputTokens)
	}
	if tokens.CacheReadInputTokens != 15000 {
		t.Errorf("CacheReadInputTokens = %d, want 15000 (deduplicated)", tokens.CacheReadInputTokens)
	}
	if tokens.CacheCreationInputTokens != 1500 {
		t.Errorf("CacheCreationInputTokens = %d, want 1500 (deduplicated)", tokens.CacheCreationInputTokens)
	}

	// Without dedup, naive sum would be: input=700, output=103, cache_read=35000, cache_create=3500
	if tokens.InputTokens == 700 {
		t.Error("InputTokens matches naive sum—deduplication is not working")
	}
}

func TestParseTranscriptUsage_EmptyMessageID(t *testing.T) {
	// Messages without an ID should be counted as unique, not discarded.
	// This was a bug found in code review: empty IDs were silently dropped.
	path := writeTranscriptFile(t,
		writeTranscriptLine("", "claude-opus-4-6", 100, 50, 5000, 1000),
		writeTranscriptLine("", "claude-opus-4-6", 200, 30, 10000, 500),
		writeTranscriptLine("msg_001", "claude-opus-4-6", 50, 20, 3000, 200),
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("parseTranscriptUsage() error: %v", err)
	}

	tokens := usage.ByModel["claude-opus-4-6"]
	if tokens == nil {
		t.Fatal("expected token data")
	}

	// All three messages counted: (100+200+50)=350 input, (50+30+20)=100 output
	if tokens.InputTokens != 350 {
		t.Errorf("InputTokens = %d, want 350 (empty IDs counted as unique)", tokens.InputTokens)
	}
	if tokens.OutputTokens != 100 {
		t.Errorf("OutputTokens = %d, want 100 (empty IDs counted as unique)", tokens.OutputTokens)
	}
}

func TestParseTranscriptUsage_MultiModel(t *testing.T) {
	path := writeTranscriptFile(t,
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 50, 5000, 1000),
		writeTranscriptLine("msg_002", "claude-sonnet-4-5", 100, 50, 5000, 1000),
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("parseTranscriptUsage() error: %v", err)
	}

	if len(usage.ByModel) != 2 {
		t.Errorf("expected 2 models, got %d", len(usage.ByModel))
	}

	opus := usage.ByModel["claude-opus-4-6"]
	sonnet := usage.ByModel["claude-sonnet-4-5"]
	if opus == nil || sonnet == nil {
		t.Fatal("expected both opus and sonnet in results")
	}

	// Cost should reflect different pricing
	cost := calculateCost(usage)
	// Opus: (100/1M * 15) + (50/1M * 75) + (5000/1M * 1.5) + (1000/1M * 18.75) = 0.0315
	// Sonnet: (100/1M * 3) + (50/1M * 15) + (5000/1M * 0.3) + (1000/1M * 3.75) = 0.0063
	expected := 0.0315 + 0.0063
	if math.Abs(cost-expected) > 0.0001 {
		t.Errorf("calculateCost() = %f, want ~%f", cost, expected)
	}
}

func TestParseTranscriptUsage_LargeBuffer(t *testing.T) {
	// Create an assistant line exceeding the old 1MB scanner limit.
	// The previous 64KB (and later 1MB) buffer caused silent failures on real transcripts
	// containing large tool results. This test verifies the 50MB buffer handles them.
	bigContent := strings.Repeat("x", 2*1024*1024) // 2MB tool result content
	bigAssistantLine := fmt.Sprintf(
		`{"type":"assistant","sessionId":"test","message":{"id":"msg_big","model":"claude-opus-4-6","role":"assistant","content":[{"type":"tool_result","content":"%s"}],"usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		bigContent,
	)

	path := writeTranscriptFile(t,
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 50, 5000, 1000),
		bigAssistantLine, // 2MB assistant message—must not be dropped or crash
		writeTranscriptLine("msg_002", "claude-opus-4-6", 200, 30, 10000, 500),
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("parseTranscriptUsage() should not error on large lines: %v", err)
	}

	tokens := usage.ByModel["claude-opus-4-6"]
	if tokens == nil {
		t.Fatal("expected token data")
	}
	// msg_001 (100 input) + msg_big (500 input) + msg_002 (200 input) = 800
	if tokens.InputTokens != 800 {
		t.Errorf("InputTokens = %d, want 800 (large assistant line must be parsed)", tokens.InputTokens)
	}
}

func TestParseTranscriptUsage_SkipsNonAssistant(t *testing.T) {
	userLine := `{"type":"user","sessionId":"test","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`
	systemLine := `{"type":"system","sessionId":"test","subtype":"turn_duration","durationMs":1000}`

	path := writeTranscriptFile(t,
		userLine,
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 50, 5000, 1000),
		systemLine,
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("parseTranscriptUsage() error: %v", err)
	}

	tokens := usage.ByModel["claude-opus-4-6"]
	if tokens == nil {
		t.Fatal("expected token data")
	}
	if tokens.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", tokens.InputTokens)
	}
}

func TestParseTranscriptUsage_UnknownModelWarning(t *testing.T) {
	path := writeTranscriptFile(t,
		writeTranscriptLine("msg_001", "claude-future-model-99", 100, 50, 5000, 1000),
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("parseTranscriptUsage() error: %v", err)
	}

	found := false
	for _, e := range usage.ParseErrors {
		if strings.Contains(e, "unknown model") && strings.Contains(e, "claude-future-model-99") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected parse error about unknown model, got none")
	}

	cost := calculateCost(usage)
	if cost <= 0 {
		t.Error("expected non-zero cost even with unknown model (default pricing)")
	}
}

func TestParseTranscriptUsage_MalformedLines(t *testing.T) {
	path := writeTranscriptFile(t,
		"this is not json",
		"",
		"{invalid json too",
		writeTranscriptLine("msg_001", "claude-opus-4-6", 100, 50, 5000, 1000),
		`{"type":"assistant","message":null}`,
	)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("should handle malformed lines gracefully: %v", err)
	}

	tokens := usage.ByModel["claude-opus-4-6"]
	if tokens == nil {
		t.Fatal("expected token data despite malformed lines")
	}
	if tokens.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", tokens.InputTokens)
	}
}

func TestParseTranscriptUsage_EmptyFile(t *testing.T) {
	path := writeTranscriptFile(t, "")

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("error on empty file: %v", err)
	}

	if len(usage.ByModel) != 0 {
		t.Errorf("expected empty ByModel, got %d", len(usage.ByModel))
	}
	if calculateCost(usage) != 0.0 {
		t.Error("expected zero cost for empty transcript")
	}
}

func TestParseTranscriptUsage_ParseErrorCap(t *testing.T) {
	// Trigger errors by using unknown model IDs (one per unique message).
	// Each unknown model adds one parse error; 20 messages exceeds maxParseErrors.
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines,
			writeTranscriptLine(fmt.Sprintf("msg_%03d", i), fmt.Sprintf("unknown-model-%d", i), 10, 5, 100, 50))
	}
	path := writeTranscriptFile(t, lines...)

	usage, err := parseTranscriptUsage(path)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	// Should have capped errors at maxParseErrors + 1 (the truncation message)
	if len(usage.ParseErrors) > maxParseErrors+1 {
		t.Errorf("ParseErrors len = %d, want <= %d (capped)", len(usage.ParseErrors), maxParseErrors+1)
	}
	// Last entry should indicate truncation
	last := usage.ParseErrors[len(usage.ParseErrors)-1]
	if !strings.Contains(last, "truncated") {
		t.Errorf("expected truncation message, got %q", last)
	}
}

func TestCostLogEntry_Schema(t *testing.T) {
	entry := CostLogEntry{
		SessionID: "hq-mayor",
		Role:      "mayor",
		Worker:    "mayor",
		CostUSD:   1.0090314,
		ModelTokens: map[string]*ModelTokenUsage{
			"claude-opus-4-6": {
				InputTokens:              249,
				OutputTokens:             34738,
				CacheReadInputTokens:     21920253,
				CacheCreationInputTokens: 397476,
			},
		},
		PricingVersion: "2026-02-17", // intentionally stale—testing schema roundtrip, not current pricing
		ParseErrors:    []string{"some messages missing model field"},
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	jsonStr := string(data)
	for _, check := range []string{"model_tokens", "pricing_version", "parse_errors", "claude-opus-4-6"} {
		if !strings.Contains(jsonStr, check) {
			t.Errorf("JSON missing expected field %s", check)
		}
	}

	var decoded CostLogEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.CostUSD != entry.CostUSD {
		t.Errorf("roundtrip CostUSD = %f, want %f", decoded.CostUSD, entry.CostUSD)
	}
	opus := decoded.ModelTokens["claude-opus-4-6"]
	if opus == nil || opus.InputTokens != 249 {
		t.Error("roundtrip lost model token data")
	}
}

func TestCostLogEntry_BackwardsCompatible(t *testing.T) {
	oldJSON := `{"session_id":"gt-mayor","role":"unknown","worker":"mayor","cost_usd":0,"ended_at":"2026-02-16T15:09:54.3036-08:00"}`

	var entry CostLogEntry
	if err := json.Unmarshal([]byte(oldJSON), &entry); err != nil {
		t.Fatalf("failed to unmarshal old-format: %v", err)
	}
	if entry.SessionID != "gt-mayor" {
		t.Errorf("SessionID = %q, want %q", entry.SessionID, "gt-mayor")
	}
	if entry.ModelTokens != nil {
		t.Error("expected nil ModelTokens for old-format entry")
	}
	if entry.ParseErrors != nil {
		t.Error("expected nil ParseErrors for old-format entry")
	}
}

func TestCostLogEntry_ModelTokensPropagation(t *testing.T) {
	// Verifies the CostLogEntry → CostEntry mapping preserves ModelTokens.
	// This is the same field-copy logic used by querySessionCostEntries.
	logEntry := CostLogEntry{
		SessionID: "test-session",
		Role:      "polecat",
		Worker:    "worker-1",
		CostUSD:   0.0378,
		ModelTokens: map[string]*ModelTokenUsage{
			"claude-opus-4-6": {
				InputTokens:              100,
				OutputTokens:             50,
				CacheReadInputTokens:     5000,
				CacheCreationInputTokens: 1000,
			},
			"claude-haiku-4-5": {
				InputTokens:  200,
				OutputTokens: 80,
			},
		},
		EndedAt: func() time.Time { t, _ := time.Parse(time.RFC3339, "2026-02-18T12:00:00Z"); return t }(),
	}

	// Serialize to JSON (simulates log file write)
	data, err := json.Marshal(logEntry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Deserialize (simulates log file read)
	var parsed CostLogEntry
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Map to CostEntry (same logic as querySessionCostEntries)
	entry := CostEntry{
		SessionID:   parsed.SessionID,
		Role:        parsed.Role,
		Worker:      parsed.Worker,
		CostUSD:     parsed.CostUSD,
		ModelTokens: parsed.ModelTokens,
		EndedAt:     parsed.EndedAt,
	}

	// Verify ModelTokens survived the pipeline
	if len(entry.ModelTokens) != 2 {
		t.Fatalf("ModelTokens has %d models, want 2", len(entry.ModelTokens))
	}
	opus := entry.ModelTokens["claude-opus-4-6"]
	if opus == nil || opus.InputTokens != 100 || opus.OutputTokens != 50 {
		t.Errorf("opus tokens not preserved: %+v", opus)
	}
	haiku := entry.ModelTokens["claude-haiku-4-5"]
	if haiku == nil || haiku.InputTokens != 200 || haiku.OutputTokens != 80 {
		t.Errorf("haiku tokens not preserved: %+v", haiku)
	}
}

func TestCalculateCost_EmptyUsage(t *testing.T) {
	if calculateCost(nil) != 0 {
		t.Error("calculateCost(nil) should be 0")
	}
	if calculateCost(&TokenUsage{ByModel: map[string]*ModelTokenUsage{}}) != 0 {
		t.Error("calculateCost(empty) should be 0")
	}
}

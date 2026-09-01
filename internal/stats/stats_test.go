package stats

import "testing"

// Per-model counters and per-channel latency percentiles are both fed by
// RecordTrace; this pins the aggregation and the percentile math.
func TestRecordTraceAggregatesModelsAndLatency(t *testing.T) {
	collector := NewCollector()
	collector.RecordTrace(RequestTrace{
		Model: "m1", FinalStatus: 200, FinalChannel: "ch",
		TotalMS: 100, PromptTokens: 10, CompletionTokens: 5,
	})
	collector.RecordTrace(RequestTrace{
		Model: "m1", FinalStatus: 500, FinalChannel: "ch",
		TotalMS: 300, PromptTokens: 20, CompletionTokens: 7,
	})
	collector.RecordTrace(RequestTrace{
		Model: "m2", FinalStatus: 200, FinalChannel: "",
		TotalMS: 50,
	})
	// Channel rows are keyed off attempt counters (the latency ring alone
	// does not create a channel row).
	collector.RecordAttempt("ch", true)
	collector.RecordAttempt("ch", false)

	summary := collector.Summary(true)

	if len(summary.Models) != 2 {
		t.Fatalf("expected 2 model rows, got %d", len(summary.Models))
	}
	// Sorted by requests desc: m1 (2) before m2 (1).
	first := summary.Models[0]
	if first.Name != "m1" || first.Requests != 2 || first.OK != 1 {
		t.Fatalf("m1 row wrong: %+v", first)
	}
	if first.PromptTokens != 30 || first.CompletionTokens != 12 {
		t.Fatalf("m1 tokens wrong: %+v", first)
	}
	if first.AvgMS != 200 {
		t.Fatalf("m1 avg latency = %d, want 200", first.AvgMS)
	}

	if len(summary.Channels) != 1 {
		t.Fatalf("expected 1 channel row, got %d", len(summary.Channels))
	}
	channel := summary.Channels[0]
	if channel.Name != "ch" {
		t.Fatalf("channel row wrong: %+v", channel)
	}
	if channel.P50MS != 100 || channel.P95MS != 300 {
		t.Fatalf("percentiles wrong: p50=%d p95=%d (want 100/300)", channel.P50MS, channel.P95MS)
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 95); got != 0 {
		t.Fatalf("percentile of empty sample = %d, want 0", got)
	}
}

func TestModelCountersPersistRoundTrip(t *testing.T) {
	collector := NewCollector()
	collector.RecordTrace(RequestTrace{Model: "m", FinalStatus: 200, FinalChannel: "ch", TotalMS: 10, PromptTokens: 3})

	restored := NewCollector()
	restored.Restore(collector.Export())

	summary := restored.Summary(true)
	if len(summary.Models) != 1 || summary.Models[0].Requests != 1 || summary.Models[0].PromptTokens != 3 {
		t.Fatalf("restored model stats wrong: %+v", summary.Models)
	}
}

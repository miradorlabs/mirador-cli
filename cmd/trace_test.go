package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

func TestTraceEventsResponsePreservesAPIEventContent(t *testing.T) {
	const body = `{
		"trace_id": "trace-1",
		"events": [{
			"event_type": "event_added",
			"severity": "info",
			"version": 2,
			"source": "client_server",
			"created_at": "2026-08-27T12:34:56Z",
			"trace_timestamp": "2026-08-27T12:34:55Z",
			"span_id": "span-1",
			"parent_span_id": "span-parent",
			"selector": "gen_ai_chat",
			"attributes": {
				"gen_ai.prompt": {"stringValue": "Explain the incident"},
				"gen_ai.usage.input_tokens": {"intValue": "9007199254740993"}
			},
			"payload": {
				"event_name": "chat.completion",
				"details": "The full completion"
			}
		}]
	}`

	var resp traceEventsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("unmarshal trace events: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(resp.Events))
	}
	if resp.TraceID != "trace-1" {
		t.Errorf("trace ID = %q, want trace-1", resp.TraceID)
	}

	event := resp.Events[0]
	if event.EventType != "event_added" {
		t.Errorf("event type = %q, want event_added", event.EventType)
	}
	if event.CreatedAt.Format(time.RFC3339) != "2026-08-27T12:34:56Z" {
		t.Errorf("created at = %s", event.CreatedAt.Format(time.RFC3339))
	}
	if event.Selector != "gen_ai_chat" || event.SpanID != "span-1" {
		t.Errorf("selector/span = %q/%q", event.Selector, event.SpanID)
	}

	var rendered bytes.Buffer
	if err := output.Render(&rendered, output.FormatJSON, output.Table{}, resp); err != nil {
		t.Fatalf("render JSON: %v", err)
	}
	for _, content := range []string{
		`"trace_id": "trace-1"`,
		`"event_type": "event_added"`,
		`"created_at": "2026-08-27T12:34:56Z"`,
		`"selector": "gen_ai_chat"`,
		`"gen_ai.prompt"`,
		`"Explain the incident"`,
		`"The full completion"`,
	} {
		if !strings.Contains(rendered.String(), content) {
			t.Errorf("rendered event does not contain %s:\n%s", content, rendered.String())
		}
	}
}

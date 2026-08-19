package cmd

import (
	"strings"
	"time"
)

// Helpers for reading loosely-typed documents. Resource commands pass the gateway's
// JSON through untouched so `-o json` stays faithful to the API, which means the
// human-facing table has to pull fields out of map[string]any without asserting a
// shape the API may have grown since.

func stringField(doc map[string]any, key string) string {
	if v, ok := doc[key].(string); ok {
		return v
	}
	return ""
}

func boolField(doc map[string]any, key string) string {
	if v, ok := doc[key].(bool); ok {
		return formatBool(v)
	}
	return ""
}

func formatBool(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// localTime renders an API timestamp in the reader's zone. An unparseable value is
// passed through rather than blanked, so an API change shows up as odd text instead
// of a silently empty column.
func localTime(value string) string {
	if value == "" {
		return ""
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return ts.Local().Format(time.RFC3339)
}

func joinStrings(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			parts = append(parts, s)
		}
	}
	return joinWith(parts, ", ")
}

func joinWith(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestReadDocument_AcceptsYAMLAndJSON keeps `apply -f` from depending on the file
// extension: YAML is a superset of JSON, so one decoder serves both and a .json file
// full of YAML (or the reverse) still works.
func TestReadDocument_AcceptsYAMLAndJSON(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "yaml", content: "title: Payments\nwidgets: []\n"},
		{name: "json", content: `{"title": "Payments", "widgets": []}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := readDocument(nil, writeTemp(t, "doc."+tc.name, tc.content))
			if err != nil {
				t.Fatalf("readDocument: %v", err)
			}
			if doc["title"] != "Payments" {
				t.Errorf("title = %v, want Payments", doc["title"])
			}
		})
	}
}

func TestReadDocument_RejectsEmptyAndUnparseable(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty", content: "   \n", want: "empty"},
		{name: "not a mapping", content: "- just\n- a list\n", want: "parse"},
		{name: "malformed", content: "title: [unclosed\n", want: "parse"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readDocument(nil, writeTemp(t, "doc.yaml", tc.content))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestReadDocument_ReadsStdin(t *testing.T) {
	doc, err := readDocument(strings.NewReader("title: from stdin\n"), "-")
	if err != nil {
		t.Fatalf("readDocument: %v", err)
	}
	if doc["title"] != "from stdin" {
		t.Errorf("title = %v", doc["title"])
	}
}

// TestResolveSlug covers the three ways a slug can arrive and the one way they can
// conflict. The file-provided slug is what makes a directory of documents
// self-describing and re-appliable without per-file arguments.
func TestResolveSlug(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		flag    string
		doc     map[string]any
		want    string
		wantErr string
	}{
		{name: "positional", args: []string{"payments"}, want: "payments"},
		{name: "flag", flag: "payments", want: "payments"},
		{name: "from the file", doc: map[string]any{"slug": "payments"}, want: "payments"},
		{name: "flag overrides the file", flag: "override", doc: map[string]any{"slug": "payments"}, want: "override"},
		{name: "positional overrides the file", args: []string{"override"}, doc: map[string]any{"slug": "payments"}, want: "override"},
		{name: "agreeing duplicates are fine", args: []string{"same"}, flag: "same", want: "same"},
		{name: "conflict is refused", args: []string{"one"}, flag: "two", wantErr: "disagree"},
		{name: "absent everywhere", doc: map[string]any{}, wantErr: "no slug"},
		{name: "empty slug in file is not a slug", doc: map[string]any{"slug": ""}, wantErr: "no slug"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.doc
			if doc == nil {
				doc = map[string]any{}
			}
			got, err := resolveSlug(tc.args, tc.flag, doc)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error mentioning %q, got %q", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSlug: %v", err)
			}
			if got != tc.want {
				t.Errorf("slug = %q, want %q", got, tc.want)
			}
		})
	}
}

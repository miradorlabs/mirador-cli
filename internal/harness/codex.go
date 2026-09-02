package harness

import "context"

// Codex is a placeholder so `mirador telemetry` lists the harness it is going to
// support before it supports it — a user who tries it gets a straight answer instead of
// "unknown harness", which reads like a typo on their end.
//
// Filling this in is the same shape as Claude: Detect shells out to the binary, Render
// maps an Exporter onto whatever variables Codex reads, and Connect merges them into
// its config file. What is missing is the vendor contract — Codex's OTLP support does
// not yet expose a documented, stable set of variable names to write, and guessing at
// them would produce a config that looks connected and exports nothing.
type Codex struct{}

func (Codex) Name() string        { return "codex" }
func (Codex) DisplayName() string { return "Codex" }

func (Codex) Detect(context.Context) Detection { return Detection{} }

func (c Codex) ConfigPath() (string, error) { return "", c.unsupported() }

func (Codex) Render(Exporter) map[string]string { return nil }

func (c Codex) Status() (Status, error) { return Status{}, c.unsupported() }

func (c Codex) ConflictsWith(Exporter) ([]Conflict, error) { return nil, c.unsupported() }

func (c Codex) Connect(Exporter, bool) error { return c.unsupported() }

func (c Codex) Disconnect() (DisconnectResult, error) { return DisconnectResult{}, c.unsupported() }

func (c Codex) unsupported() error {
	return &ErrUnsupported{
		Harness: c.DisplayName(),
		Reason:  "it does not yet expose a stable set of OTLP environment variables to configure",
	}
}

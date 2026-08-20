package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/api"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

type streamLogEvent struct {
	Log    logEntry `json:"log"`
	Replay bool     `json:"replay"`
}

// streamWindows are the relative windows the gateway accepts. Rejecting an unknown
// one locally turns a 400 into a message that lists the choices.
var streamWindows = []string{"1m", "5m", "15m", "1h", "4h", "12h", "24h", "7d", "30d"}

func newLogTailCommand() *cobra.Command {
	var (
		filter    string
		window    string
		pageSize  int
		heartbeat int
	)

	cmd := &cobra.Command{
		Use:   "tail",
		Short: "Follow matching logs in real time",
		Long: `Replays a bounded window oldest-first, then streams matching logs as they arrive.

Delivery is best effort, not at-least-once. The tail advances over event time, so
a record that becomes visible after the cursor has passed its timestamp — a
clock-skewed client, a lagging replica — never appears here, and a burst larger
than --page-size drops its middle so the tail stays current rather than falling
behind ingest. Records may also repeat after a reconnect. Use ` + "`mirador log query`" + `
when completeness matters.

The connection resumes automatically after a drop, and the server closes it after
one hour by design. Ctrl-C to stop.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !validWindow(window) {
				return fmt.Errorf("unknown window %q (want one of %s)", window, joinWith(streamWindows, ", "))
			}

			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}
			// Ctrl-C ends the stream rather than killing the process mid-frame, so a
			// partially written record never reaches stdout.
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()

			query := url.Values{}
			addIfSet(query, "filter", filter)
			query.Set("window", window)
			if pageSize > 0 {
				query.Set("page_size", strconv.Itoa(pageSize))
			}
			if heartbeat > 0 {
				query.Set("heartbeat", strconv.Itoa(heartbeat))
			}

			return followLogs(cmd, ctx, client, query, format)
		},
	}

	cmd.Flags().StringVar(&filter, "filter", "", "AIP-160 filter, the same expression `log query` accepts")
	cmd.Flags().StringVar(&window, "window", "5m", "window replayed on connect: "+joinWith(streamWindows, ", "))
	cmd.Flags().IntVar(&pageSize, "page-size", 0, "max replay records and per-poll live cap (1-1000)")
	cmd.Flags().IntVar(&heartbeat, "heartbeat", 0, "heartbeat interval in seconds (5-60)")
	return cmd
}

// followLogs runs the read loop, reconnecting with Last-Event-ID so a dropped
// connection resumes after the last record seen instead of replaying the window.
func followLogs(cmd *cobra.Command, ctx context.Context, client *api.Client, query url.Values, format output.Format) error {
	var lastID string

	for {
		stream, err := client.Stream(ctx, "/v1/logs/stream", query, lastID)
		if err != nil {
			return err
		}

		lastID, err = drainStream(cmd, stream, lastID, format)
		stream.Close()

		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		// The server closes the connection after an hour; reconnecting is the
		// documented continuation, not an error worth reporting to the user.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func drainStream(cmd *cobra.Command, stream *api.Stream, lastID string, format output.Format) (string, error) {
	out := cmd.OutOrStdout()
	encoder := json.NewEncoder(out)

	for {
		event, err := stream.Next()
		if err != nil {
			return lastID, err
		}

		switch event.Name {
		case "log":
			var payload streamLogEvent
			if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
				// One malformed frame should not end a tail that is otherwise healthy.
				fmt.Fprintf(cmd.ErrOrStderr(), "skipping unreadable log frame: %v\n", err)
				continue
			}
			if event.ID != "" {
				lastID = event.ID
			}
			if format == output.FormatTable {
				fmt.Fprintln(out, formatLogLine(payload.Log))
				continue
			}
			// JSONL rather than a JSON array: a stream has no end, so it cannot be
			// one document. Each line is independently parseable.
			if err := encoder.Encode(payload.Log); err != nil {
				return lastID, err
			}
		case "error":
			return lastID, fmt.Errorf("stream ended: %s", event.Data)
		case "ready", "heartbeat", "":
			// Metadata and keep-alives carry nothing the caller asked for.
		}
	}
}

func formatLogLine(entry logEntry) string {
	severity := entry.SeverityText
	if severity == "" {
		severity = "-"
	}
	service := entry.ServiceName
	if service == "" {
		service = "-"
	}
	// The body and service come from ingested telemetry, so they are sanitized before
	// reaching the terminal — this line is printed directly rather than through the
	// table renderer that would otherwise strip control characters.
	return fmt.Sprintf("%s  %-5s  %-24s  %s",
		entry.Time.Local().Format(time.RFC3339),
		output.SanitizeTerminal(severity),
		output.SanitizeTerminal(output.Truncate(service, 24)),
		output.SanitizeTerminal(entry.Body))
}

func validWindow(window string) bool {
	return slices.Contains(streamWindows, window)
}

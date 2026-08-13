package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

type trace struct {
	TraceID         string            `json:"trace_id"`
	TraceNumber     uint64            `json:"trace_number"`
	Name            string            `json:"name"`
	Status          string            `json:"status"`
	DurationMs      int64             `json:"duration_ms"`
	CreatedAt       time.Time         `json:"created_at"`
	HighestSeverity string            `json:"highest_severity,omitempty"`
	Tags            []string          `json:"tags,omitempty"`
	Attributes      map[string]string `json:"attributes,omitempty"`
}

type listTracesResponse struct {
	Traces        []trace `json:"traces"`
	Total         int64   `json:"total"`
	ProjectID     string  `json:"project_id"`
	NextPageToken string  `json:"next_page_token,omitempty"`
}

type traceResponse struct {
	Trace     trace  `json:"trace"`
	ProjectID string `json:"project_id"`
}

func newTraceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "trace",
		Aliases: []string{"traces"},
		Short:   "Query traces",
	}
	cmd.AddCommand(newTraceListCommand(), newTraceGetCommand(), newTraceEventsCommand(), newTraceTagsCommand())
	return cmd
}

func newTraceListCommand() *cobra.Command {
	var filter, since, until, pageToken string
	var pageSize int

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List traces matching a filter",
		Long: `Lists traces newest first.

--filter takes an AIP-160 expression, e.g. status="running" or
attribute.customer="acme". Run ` + "`mirador trace tags`" + ` to discover what you
can filter on before guessing at names.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			query := url.Values{}
			setIfNotEmpty(query, "filter", filter)
			setIfNotEmpty(query, "since", since)
			setIfNotEmpty(query, "until", until)
			setIfNotEmpty(query, "page_token", pageToken)
			if pageSize > 0 {
				query.Set("page_size", strconv.Itoa(pageSize))
			}

			var resp listTracesResponse
			if err := client.Get(ctx, "/v1/traces", query, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Traces))
			for _, t := range resp.Traces {
				rows = append(rows, []string{
					t.TraceID,
					output.Truncate(t.Name, 40),
					t.Status,
					formatDuration(t.DurationMs),
					t.CreatedAt.Local().Format(time.RFC3339),
				})
			}

			if err := output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"TRACE ID", "NAME", "STATUS", "DURATION", "CREATED"},
				Rows:    rows,
			}, resp); err != nil {
				return err
			}

			// The token is the only way to continue, and a table view would otherwise
			// drop it silently and look like the end of the results.
			if format == output.FormatTable && resp.NextPageToken != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "\n%d of %d shown. Next page: --page-token %s\n",
					len(resp.Traces), resp.Total, resp.NextPageToken)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&filter, "filter", "f", "", "AIP-160 filter expression")
	cmd.Flags().StringVar(&since, "since", "", "start of the window (RFC 3339 or a relative age like 2h)")
	cmd.Flags().StringVar(&until, "until", "", "end of the window (RFC 3339 or a relative age)")
	cmd.Flags().IntVar(&pageSize, "page-size", 0, "results per page")
	cmd.Flags().StringVar(&pageToken, "page-token", "", "continue from a previous page")
	return cmd
}

func newTraceGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <trace-id>",
		Short: "Show one trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			var resp traceResponse
			if err := client.Get(ctx, "/v1/traces/"+url.PathEscape(args[0]), nil, &resp); err != nil {
				return err
			}

			t := resp.Trace
			pairs := [][2]string{
				{"trace id", t.TraceID},
				{"name", t.Name},
				{"status", t.Status},
				{"duration", formatDuration(t.DurationMs)},
				{"created", t.CreatedAt.Local().Format(time.RFC3339)},
			}
			if t.HighestSeverity != "" {
				pairs = append(pairs, [2]string{"highest severity", t.HighestSeverity})
			}
			for _, tag := range t.Tags {
				pairs = append(pairs, [2]string{"tag", tag})
			}
			return output.KeyValues(cmd.OutOrStdout(), format, pairs, resp)
		},
	}
}

type traceEvent struct {
	EventID   string    `json:"event_id,omitempty"`
	Type      string    `json:"type,omitempty"`
	Name      string    `json:"name,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type traceEventsResponse struct {
	Events    []traceEvent `json:"events"`
	ProjectID string       `json:"project_id"`
}

func newTraceEventsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "events <trace-id>",
		Short: "List the events on a trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			var resp traceEventsResponse
			path := "/v1/traces/" + url.PathEscape(args[0]) + "/events"
			if err := client.Get(ctx, path, nil, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Events))
			for _, e := range resp.Events {
				rows = append(rows, []string{
					e.Timestamp.Local().Format(time.RFC3339),
					e.Type,
					output.Truncate(e.Name, 48),
					e.Severity,
				})
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"TIME", "TYPE", "NAME", "SEVERITY"},
				Rows:    rows,
			}, resp)
		},
	}
}

type traceTagsResponse struct {
	Tags struct {
		Items     []string `json:"items"`
		Truncated bool     `json:"truncated"`
	} `json:"tags"`
	ProjectID string `json:"project_id"`
}

func newTraceTagsCommand() *cobra.Command {
	var since, until string

	cmd := &cobra.Command{
		Use:   "tags",
		Short: "List the trace tags available to filter on",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			query := url.Values{}
			setIfNotEmpty(query, "since", since)
			setIfNotEmpty(query, "until", until)

			var resp traceTagsResponse
			if err := client.Get(ctx, "/v1/traces/tags", query, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Tags.Items))
			for _, tag := range resp.Tags.Items {
				rows = append(rows, []string{tag})
			}
			if err := output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"TAG"},
				Rows:    rows,
			}, resp); err != nil {
				return err
			}
			// Truncation changes what an absent tag means, so it cannot be left implicit.
			if format == output.FormatTable && resp.Tags.Truncated {
				fmt.Fprintln(cmd.ErrOrStderr(), "\nList truncated — narrow the window with --since to see the rest.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "start of the window (RFC 3339 or a relative age)")
	cmd.Flags().StringVar(&until, "until", "", "end of the window (RFC 3339 or a relative age)")
	return cmd
}

func formatDuration(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

func setIfNotEmpty(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

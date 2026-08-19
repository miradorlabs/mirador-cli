package cmd

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

type logEntry struct {
	Time           time.Time         `json:"time"`
	SeverityText   string            `json:"severity_text"`
	SeverityNumber int64             `json:"severity_number"`
	ServiceName    string            `json:"service_name"`
	Body           string            `json:"body"`
	TraceID        string            `json:"trace_id,omitempty"`
	SpanID         string            `json:"span_id,omitempty"`
	Attributes     map[string]string `json:"attributes,omitempty"`
}

type queryLogsResponse struct {
	Logs          []logEntry `json:"logs"`
	ProjectID     string     `json:"project_id"`
	RangeStart    time.Time  `json:"range_start"`
	RangeEnd      time.Time  `json:"range_end"`
	NextPageToken string     `json:"next_page_token,omitempty"`
}

func newLogCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "log",
		Aliases: []string{"logs"},
		Short:   "Query OpenTelemetry logs",
	}
	cmd.AddCommand(newLogQueryCommand(), newLogStatsCommand(), newLogAttributesCommand(), newLogTailCommand())
	return cmd
}

func newLogQueryCommand() *cobra.Command {
	var filter, since, until, pageToken string
	var pageSize int

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query logs, newest first",
		Long: `Reads the last hour unless --since or --until narrows it.

The window may span at most 35 days, and the response echoes the window it
actually read — worth checking before concluding a service is quiet.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			query := url.Values{}
			setIfNotEmpty(query, "filter", filter)
			setIfNotEmpty(query, "page_token", pageToken)
			// The gateway rejects a window alongside a page token, since only one
			// could win; the token already carries the window it was issued for.
			if pageToken == "" {
				setIfNotEmpty(query, "since", since)
				setIfNotEmpty(query, "until", until)
			}
			if pageSize > 0 {
				query.Set("page_size", strconv.Itoa(pageSize))
			}

			var resp queryLogsResponse
			if err := client.Get(ctx, "/v1/logs", query, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Logs))
			for _, l := range resp.Logs {
				rows = append(rows, []string{
					l.Time.Local().Format(time.RFC3339),
					l.SeverityText,
					output.Truncate(l.ServiceName, 24),
					output.Truncate(l.Body, 80),
				})
			}

			if err := output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"TIME", "SEVERITY", "SERVICE", "BODY"},
				Rows:    rows,
			}, resp); err != nil {
				return err
			}

			if format == output.FormatTable {
				fmt.Fprintf(cmd.ErrOrStderr(), "\nWindow: %s to %s\n",
					resp.RangeStart.Local().Format(time.RFC3339), resp.RangeEnd.Local().Format(time.RFC3339))
				if resp.NextPageToken != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "Next page: --page-token %s\n", resp.NextPageToken)
				}
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

type logStatsResponse struct {
	Buckets    []map[string]any `json:"buckets"`
	Groups     []map[string]any `json:"groups"`
	ProjectID  string           `json:"project_id"`
	RangeStart time.Time        `json:"range_start"`
	RangeEnd   time.Time        `json:"range_end"`
}

func newLogStatsCommand() *cobra.Command {
	var filter, since, until, interval, groupBy string
	var topK int

	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Summarize log volume over a window",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			query := url.Values{}
			setIfNotEmpty(query, "filter", filter)
			setIfNotEmpty(query, "since", since)
			setIfNotEmpty(query, "until", until)
			setIfNotEmpty(query, "interval", interval)
			setIfNotEmpty(query, "group_by", groupBy)
			if topK > 0 {
				query.Set("top_k", strconv.Itoa(topK))
			}

			var resp logStatsResponse
			if err := client.Get(ctx, "/v1/logs/stats", query, &resp); err != nil {
				return err
			}

			// The bucket and group shapes vary with group_by, so the human view stays a
			// summary and the full payload is available via -o json.
			rows := [][]string{
				{"buckets", strconv.Itoa(len(resp.Buckets))},
				{"groups", strconv.Itoa(len(resp.Groups))},
				{"window", resp.RangeStart.Local().Format(time.RFC3339) + " to " + resp.RangeEnd.Local().Format(time.RFC3339)},
			}
			if format == output.FormatTable {
				fmt.Fprintln(cmd.ErrOrStderr(), "Use -o json for the full histogram.")
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"FIELD", "VALUE"},
				Rows:    rows,
			}, resp)
		},
	}

	cmd.Flags().StringVarP(&filter, "filter", "f", "", "AIP-160 filter expression")
	cmd.Flags().StringVar(&since, "since", "", "start of the window (RFC 3339 or a relative age)")
	cmd.Flags().StringVar(&until, "until", "", "end of the window (RFC 3339 or a relative age)")
	cmd.Flags().StringVar(&interval, "interval", "", "bucket width, e.g. 5m")
	cmd.Flags().StringVar(&groupBy, "group-by", "", "dimension to group by")
	cmd.Flags().IntVar(&topK, "top-k", 0, "keep only the top K groups")
	return cmd
}

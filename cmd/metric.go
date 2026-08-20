package cmd

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

type metric struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Unit       string `json:"unit,omitempty"`
	Dimensions struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	} `json:"dimensions"`
}

type metricCatalogResponse struct {
	Metrics struct {
		Items     []metric `json:"items"`
		Truncated bool     `json:"truncated"`
	} `json:"metrics"`
	ProjectID  string    `json:"project_id"`
	RangeStart time.Time `json:"range_start"`
	RangeEnd   time.Time `json:"range_end"`
}

// promResponse is the Prometheus /api/v1/query envelope the gateway mirrors.
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string           `json:"resultType"`
		Result     []map[string]any `json:"result"`
	} `json:"data"`
	ErrorType string `json:"errorType,omitempty"`
	Error     string `json:"error,omitempty"`
}

func newMetricCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "metric",
		Aliases: []string{"metrics"},
		Short:   "Explore and query metrics with PromQL",
	}
	cmd.AddCommand(newMetricListCommand(), newMetricQueryCommand(), newMetricRangeCommand())
	return cmd
}

func newMetricListCommand() *cobra.Command {
	var since, until string

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List metric names, kinds, and dimensions",
		Long: `The catalogue is how you compose a correct PromQL query: the kind tells
you the idiom — a gauge reads directly, a sum wants rate(), a histogram wants
histogram_quantile().`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			query := url.Values{}
			setIfNotEmpty(query, "since", since)
			setIfNotEmpty(query, "until", until)

			var resp metricCatalogResponse
			if err := client.Get(ctx, "/v1/metrics", query, &resp); err != nil {
				return err
			}

			rows := make([][]string, 0, len(resp.Metrics.Items))
			for _, m := range resp.Metrics.Items {
				names := make([]string, 0, len(m.Dimensions.Items))
				for _, d := range m.Dimensions.Items {
					names = append(names, d.Name)
				}
				rows = append(rows, []string{
					m.Name,
					m.Kind,
					m.Unit,
					output.Truncate(strings.Join(names, ","), 40),
				})
			}

			if err := output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: []string{"NAME", "KIND", "UNIT", "DIMENSIONS"},
				Rows:    rows,
			}, resp); err != nil {
				return err
			}
			// A truncated catalogue means an absent metric name proves nothing.
			if format == output.FormatTable && resp.Metrics.Truncated {
				fmt.Fprintln(cmd.ErrOrStderr(), "\nCatalogue truncated — narrow the window with --since.")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "start of the window (RFC 3339 or a relative age)")
	cmd.Flags().StringVar(&until, "until", "", "end of the window (RFC 3339 or a relative age)")
	return cmd
}

func newMetricQueryCommand() *cobra.Command {
	var at string

	cmd := &cobra.Command{
		Use:   "query <promql>",
		Short: "Evaluate a PromQL expression at one instant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			query := url.Values{}
			query.Set("query", args[0])
			setIfNotEmpty(query, "time", at)

			var resp promResponse
			if err := client.Get(ctx, "/v1/metrics/query", query, &resp); err != nil {
				return err
			}
			return renderPromResult(cmd, format, resp)
		},
	}

	cmd.Flags().StringVar(&at, "at", "", "instant to evaluate at (RFC 3339 or a relative age); defaults to now")
	return cmd
}

func newMetricRangeCommand() *cobra.Command {
	var start, end, step string

	cmd := &cobra.Command{
		Use:   "range <promql>",
		Short: "Evaluate a PromQL expression over a time range",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			query := url.Values{}
			query.Set("query", args[0])
			setIfNotEmpty(query, "start", start)
			setIfNotEmpty(query, "end", end)
			setIfNotEmpty(query, "step", step)

			var resp promResponse
			if err := client.Get(ctx, "/v1/metrics/query_range", query, &resp); err != nil {
				return err
			}
			return renderPromResult(cmd, format, resp)
		},
	}

	cmd.Flags().StringVar(&start, "start", "", "range start (RFC 3339 or a relative age)")
	cmd.Flags().StringVar(&end, "end", "", "range end (RFC 3339 or a relative age)")
	cmd.Flags().StringVar(&step, "step", "", "resolution step, e.g. 1m")
	return cmd
}

// renderPromResult summarizes each series for the table view. The samples
// themselves are structurally variable (scalar, vector, matrix), so anything
// beyond a per-series summary is left to -o json.
func renderPromResult(cmd *cobra.Command, format output.Format, resp promResponse) error {
	rows := make([][]string, 0, len(resp.Data.Result))
	for _, series := range resp.Data.Result {
		labels := ""
		if raw, ok := series["metric"].(map[string]any); ok {
			parts := make([]string, 0, len(raw))
			for k, v := range raw {
				parts = append(parts, fmt.Sprintf("%s=%v", k, v))
			}
			labels = strings.Join(parts, ",")
		}
		value := ""
		if v, ok := series["value"].([]any); ok && len(v) == 2 {
			value = fmt.Sprintf("%v", v[1])
		} else if v, ok := series["values"].([]any); ok {
			value = fmt.Sprintf("%d samples", len(v))
		}
		rows = append(rows, []string{output.Truncate(labels, 64), value})
	}

	if format == output.FormatTable && len(rows) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "The query matched nothing. That is a valid answer, not an error — check the metric name with `mirador metric list`.")
	}
	return output.Render(cmd.OutOrStdout(), format, output.Table{
		Headers: []string{"SERIES", "VALUE"},
		Rows:    rows,
	}, resp)
}

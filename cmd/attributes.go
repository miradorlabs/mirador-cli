package cmd

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/miradorlabs/mirador-cli/internal/output"
)

// stringCatalog is the uniform bounded-collection envelope the catalogue endpoints
// use. Truncated matters: it is the difference between "this key has no values" and
// "the sample did not reach this key", and treating the two alike is how a filter
// gets written against a value that does exist.
type stringCatalog struct {
	Items     []string `json:"items"`
	Truncated bool     `json:"truncated"`
}

type attributeEntry struct {
	Key    string        `json:"key"`
	Values stringCatalog `json:"values"`
}

type attributeCatalog struct {
	Items     []attributeEntry `json:"items"`
	Truncated bool             `json:"truncated"`
}

type traceAttributesResponse struct {
	ProjectID  string           `json:"project_id"`
	RangeStart string           `json:"range_start"`
	RangeEnd   string           `json:"range_end"`
	Attributes attributeCatalog `json:"attributes"`
}

type logAttributesResponse struct {
	ProjectID    string           `json:"project_id"`
	RangeStart   string           `json:"range_start"`
	RangeEnd     string           `json:"range_end"`
	Attributes   attributeCatalog `json:"attributes"`
	ServiceNames stringCatalog    `json:"service_names"`
}

func newTraceAttributesCommand() *cobra.Command {
	var since, until string

	cmd := &cobra.Command{
		Use:   "attributes",
		Short: "List trace attribute keys and the values seen for them",
		Long: `The keys usable as ` + "`attribute.<key>=...`" + ` in a trace filter.

Defaults to the last 30 days, which is also the maximum span. The window is a
discovery aid rather than an index: a key missing here can still match older
traces, so an empty result is not proof of absence.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			query := url.Values{}
			addIfSet(query, "since", since)
			addIfSet(query, "until", until)

			var resp traceAttributesResponse
			if err := client.Get(ctx, "/v1/traces/attributes", query, &resp); err != nil {
				return err
			}

			warnTruncated(cmd, format, resp.Attributes.Truncated, "attribute keys")
			return output.Render(cmd.OutOrStdout(), format, attributeTable(resp.Attributes), resp)
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "inclusive start: RFC 3339 or relative age (7d, 2h)")
	cmd.Flags().StringVar(&until, "until", "", "exclusive end: RFC 3339 or relative age")
	return cmd
}

func newLogAttributesCommand() *cobra.Command {
	var since, until string
	var servicesOnly bool

	cmd := &cobra.Command{
		Use:   "attributes",
		Short: "List log attribute keys, and the services that emit logs",
		Long: `The discovery step before writing an ` + "`attribute.<key>`" + ` filter or a group-by.

Also answers "which services exist" via --services, which no other endpoint can:
stats grouping caps results per bucket, so a project with more services than the
cap can never enumerate them that way.

Which keys exist is complete for the window; values are sampled from the most
recent records. A key showing no values with truncated set means the sample did
not reach it, not that the key is valueless. Defaults to 24 hours, max 7 days.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand(cmd)
			if err != nil {
				return err
			}

			query := url.Values{}
			addIfSet(query, "since", since)
			addIfSet(query, "until", until)

			var resp logAttributesResponse
			if err := client.Get(ctx, "/v1/logs/attributes", query, &resp); err != nil {
				return err
			}

			if servicesOnly {
				warnTruncated(cmd, format, resp.ServiceNames.Truncated, "service names")
				rows := make([][]string, 0, len(resp.ServiceNames.Items))
				for _, name := range resp.ServiceNames.Items {
					rows = append(rows, []string{name})
				}
				return output.Render(cmd.OutOrStdout(), format, output.Table{
					Headers: []string{"SERVICE"},
					Rows:    rows,
				}, resp.ServiceNames)
			}

			warnTruncated(cmd, format, resp.Attributes.Truncated, "attribute keys")
			return output.Render(cmd.OutOrStdout(), format, attributeTable(resp.Attributes), resp)
		},
	}

	cmd.Flags().StringVar(&since, "since", "", "inclusive start: RFC 3339 or relative age (24h, 7d)")
	cmd.Flags().StringVar(&until, "until", "", "exclusive end: RFC 3339 or relative age")
	cmd.Flags().BoolVar(&servicesOnly, "services", false, "list the service inventory instead of attribute keys")
	return cmd
}

func attributeTable(catalog attributeCatalog) output.Table {
	rows := make([][]string, 0, len(catalog.Items))
	for _, entry := range catalog.Items {
		values := output.Truncate(joinWith(entry.Values.Items, ", "), 60)
		if entry.Values.Truncated {
			values += " …"
		}
		rows = append(rows, []string{
			entry.Key,
			strconv.Itoa(len(entry.Values.Items)),
			values,
		})
	}
	return output.Table{Headers: []string{"KEY", "VALUES", "SAMPLE"}, Rows: rows}
}

// warnTruncated says so on stderr when a catalogue was cut short. It goes to stderr
// and only in table mode so that piping to jq is never polluted; machine consumers
// read the `truncated` field itself.
func warnTruncated(cmd *cobra.Command, format output.Format, truncated bool, what string) {
	if truncated && format == output.FormatTable {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: more %s exist than are shown (truncated)\n", what)
	}
}

func addIfSet(query url.Values, key, value string) {
	if value != "" {
		query.Set(key, value)
	}
}

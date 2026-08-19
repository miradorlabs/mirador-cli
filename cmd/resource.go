package cmd

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/miradorlabs/mirador-cli/internal/api"
	"github.com/miradorlabs/mirador-cli/internal/output"
)

// resource describes a slug-addressed collection served by the API gateway's
// conditional-write contract: list, get, PUT with exactly one precondition, and
// DELETE gated on If-Match. Dashboards, metric alerts and derived metrics implement
// it identically, so the commands are generated once from this description.
//
// Documents move through as map[string]any rather than typed structs so that
// `-o json` returns the gateway's document verbatim, including fields added to the
// API after this CLI was built.
type resource struct {
	singular   string
	plural     string
	aliases    []string
	path       string
	collection string // the array's key in the list response
	short      string
	long       string

	listHeaders []string
	listRow     func(item map[string]any) []string
	detail      func(doc map[string]any) [][2]string
}

func (r resource) command(extra ...*cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:     r.singular,
		Aliases: append([]string{r.plural}, r.aliases...),
		Short:   r.short,
		Long:    r.long,
	}
	cmd.AddCommand(r.listCommand(), r.getCommand(), r.applyCommand(), r.deleteCommand())
	cmd.AddCommand(extra...)
	return cmd
}

// applyResult is what a machine consumer of `apply` receives: the outcome, the new
// concurrency token, and the document the gateway stored.
type applyResult struct {
	Slug     string         `json:"slug"`
	Result   string         `json:"result"`
	ETag     string         `json:"etag,omitempty"`
	Document map[string]any `json:"document,omitempty"`
}

func (r resource) itemPath(slug string) string {
	return r.path + "/" + url.PathEscape(slug)
}

func (r resource) listCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List " + r.plural + " in the current project",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			var resp map[string]any
			if err := client.Get(ctx, r.path, nil, &resp); err != nil {
				return err
			}

			items, _ := resp[r.collection].([]any)
			rows := make([][]string, 0, len(items))
			for _, raw := range items {
				if item, ok := raw.(map[string]any); ok {
					rows = append(rows, r.listRow(item))
				}
			}
			return output.Render(cmd.OutOrStdout(), format, output.Table{
				Headers: r.listHeaders,
				Rows:    rows,
			}, resp)
		},
	}
}

func (r resource) getCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <slug>",
		Short: "Show one " + r.singular + " in full",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			var doc map[string]any
			meta, err := client.GetWithMeta(ctx, r.itemPath(args[0]), nil, &doc)
			if err != nil {
				return err
			}

			pairs := r.detail(doc)
			// The ETag is the token a later apply or delete must present, so it belongs
			// in the human view rather than only in -o json.
			if meta.ETag != "" {
				pairs = append(pairs, [2]string{"etag", meta.ETag})
			}
			return output.KeyValues(cmd.OutOrStdout(), format, pairs, doc)
		},
	}
}

func (r resource) applyCommand() *cobra.Command {
	var (
		file       string
		slugFlag   string
		etag       string
		createOnly bool
	)

	cmd := &cobra.Command{
		Use:   "apply [slug]",
		Short: "Create or replace a " + r.singular + " from a file",
		Long: `Reads a ` + r.singular + ` document from a YAML or JSON file and writes it.

The slug is the identity: pass it as an argument, set it with --slug, or put a
top-level ` + "`slug:`" + ` in the file itself so the file is self-describing. The API
takes the slug from the URL and rejects it in the body, so it is stripped before
the request.

Writes are conditional, never blind. By default apply reads the current revision
first and replaces exactly that one, so a concurrent change fails loudly instead
of being silently overwritten. Re-applying an unchanged document is a no-op.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := readDocument(cmd.InOrStdin(), file)
			if err != nil {
				return err
			}

			slug, err := resolveSlug(args, slugFlag, doc)
			if err != nil {
				return err
			}
			// The body must not carry the slug: every Input schema is
			// additionalProperties:false, so leaving it in is a 400.
			delete(doc, "slug")

			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			pre, err := r.precondition(ctx, client, slug, etag, createOnly)
			if err != nil {
				return err
			}

			var result map[string]any
			meta, err := client.Put(ctx, r.itemPath(slug), pre, doc, &result)
			if err != nil {
				if api.IsPreconditionFailed(err) {
					if createOnly {
						return fmt.Errorf("%s %q already exists — drop --create to replace it", r.singular, slug)
					}
					return fmt.Errorf("%s %q changed since it was read; re-run to apply on top of the new revision", r.singular, slug)
				}
				return err
			}

			verb := "replaced"
			if meta.Created() {
				verb = "created"
			}
			pairs := [][2]string{
				{"slug", slug},
				{"result", verb},
				{"etag", meta.ETag},
			}
			// The outcome travels in the machine payload too. Without it a caller
			// parsing -o json sees only the stored document and cannot tell a create
			// from a replace — the one thing apply is being asked.
			return output.KeyValues(cmd.OutOrStdout(), format, pairs, applyResult{
				Slug:     slug,
				Result:   verb,
				ETag:     meta.ETag,
				Document: result,
			})
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "YAML or JSON document to apply, or - for stdin")
	cmd.Flags().StringVar(&slugFlag, "slug", "", "slug to write to (overrides any slug in the file)")
	cmd.Flags().StringVar(&etag, "etag", "", "replace only this exact revision, skipping the read")
	cmd.Flags().BoolVar(&createOnly, "create", false, "fail if the slug already exists")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// precondition decides what the write asserts. An explicit --etag or --create is
// taken at face value; otherwise the current revision is read so the write can name
// it, which is what makes a concurrent change a 412 rather than a lost update.
func (r resource) precondition(ctx context.Context, client *api.Client, slug, etag string, createOnly bool) (api.Precondition, error) {
	switch {
	case createOnly && etag != "":
		return api.Precondition{}, fmt.Errorf("--create and --etag are contradictory: one adds, the other replaces")
	case createOnly:
		return api.Precondition{CreateOnly: true}, nil
	case etag != "":
		return api.Precondition{ReplaceETag: etag}, nil
	}

	meta, err := client.GetWithMeta(ctx, r.itemPath(slug), nil, nil)
	if err != nil {
		if api.IsNotFound(err) {
			return api.Precondition{CreateOnly: true}, nil
		}
		return api.Precondition{}, err
	}
	if meta.ETag == "" {
		return api.Precondition{}, fmt.Errorf("the gateway returned no etag for %s %q, so a safe replace is not possible", r.singular, slug)
	}
	return api.Precondition{ReplaceETag: meta.ETag}, nil
}

func (r resource) deleteCommand() *cobra.Command {
	var etag string

	cmd := &cobra.Command{
		Use:   "delete <slug>",
		Short: "Delete a " + r.singular,
		Long: `Deletes the current revision.

The delete is conditional: it names the revision it read, so it cannot remove a
change made after you looked. Pass --etag to assert a specific revision instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, client, format, err := setupProjectCommand()
			if err != nil {
				return err
			}

			slug := args[0]
			target := etag
			if target == "" {
				meta, err := client.GetWithMeta(ctx, r.itemPath(slug), nil, nil)
				if err != nil {
					return err
				}
				target = meta.ETag
			}

			if err := client.Delete(ctx, r.itemPath(slug), target); err != nil {
				if api.IsPreconditionFailed(err) {
					return fmt.Errorf("%s %q changed since it was read; re-run to delete the current revision", r.singular, slug)
				}
				return err
			}
			return output.KeyValues(cmd.OutOrStdout(), format, [][2]string{
				{"slug", slug},
				{"result", "deleted"},
			}, map[string]string{"slug": slug, "result": "deleted"})
		},
	}

	cmd.Flags().StringVar(&etag, "etag", "", "delete only this exact revision, skipping the read")
	return cmd
}

// readDocument parses a YAML or JSON document. YAML is a superset of JSON, so one
// decoder handles both and the file's extension never has to be trusted.
func readDocument(stdin io.Reader, path string) (map[string]any, error) {
	var (
		raw []byte
		err error
	)
	if path == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, fmt.Errorf("%s is empty", describeSource(path))
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", describeSource(path), err)
	}
	if doc == nil {
		return nil, fmt.Errorf("%s does not contain a document", describeSource(path))
	}
	return doc, nil
}

func describeSource(path string) string {
	if path == "-" {
		return "stdin"
	}
	return path
}

func resolveSlug(args []string, flag string, doc map[string]any) (string, error) {
	if len(args) == 1 && flag != "" && args[0] != flag {
		return "", fmt.Errorf("slug given twice and they disagree: %q and --slug %q", args[0], flag)
	}
	if flag != "" {
		return flag, nil
	}
	if len(args) == 1 {
		return args[0], nil
	}
	if slug, ok := doc["slug"].(string); ok && slug != "" {
		return slug, nil
	}
	return "", fmt.Errorf("no slug: pass it as an argument, set --slug, or add a top-level `slug:` to the file")
}

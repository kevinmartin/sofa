package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/admission"
)

type specDigestOptions struct {
	title    string
	bodyFile string
}

func newSpecDigestCommand() *cobra.Command {
	var opts specDigestOptions
	cmd := newStageCommand("spec-digest", "Print the canonical digest of an issue specification", "spec-digest requires --title and --body-file", func(cmd *cobra.Command, _ []string) error {
		if opts.title == "" || opts.bodyFile == "" {
			return errors.New("spec-digest requires --title and --body-file")
		}
		return runSpecDigest(opts, cmd.OutOrStdout())
	})
	cmd.Flags().StringVar(&opts.title, "title", "", "Approved issue title")
	cmd.Flags().StringVar(&opts.bodyFile, "body-file", "", "Path to the approved issue body")
	return cmd
}

func runSpecDigest(opts specDigestOptions, output io.Writer) error {
	fh, err := os.Open(opts.bodyFile)
	if err != nil {
		return errors.New("cannot read issue body")
	}
	defer fh.Close()
	content, err := io.ReadAll(io.LimitReader(fh, (64<<10)+1))
	if err != nil || len(content) > 64<<10 {
		return errors.New("issue body unavailable or oversized")
	}
	_, digest, err := admission.CanonicalSpec(opts.title, string(content))
	if err != nil {
		return err
	}
	fmt.Fprintln(output, digest)
	return nil
}

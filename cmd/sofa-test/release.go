package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/release"
)

// release-observe is read-only evidence collection for a designated canary PR.
// It never constructs a sofa attempt, changes a board item, or starts a release.
func newReleaseObserveCommand() *cobra.Command {
	var repo, mergeSHA string
	var number int64
	var checks []string
	cmd := &cobra.Command{
		Use:   "release-observe",
		Short: "Inspect a designated PR's exact merge and release checks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repo == "" || number < 1 {
				return errors.New("release-observe requires --repository and --pr")
			}
			required := make([]release.RequiredCheck, 0, len(checks))
			for _, raw := range checks {
				name, idText, ok := strings.Cut(raw, "@")
				id, err := strconv.ParseInt(idText, 10, 64)
				if !ok || err != nil || id < 1 || name == "" {
					return errors.New("invalid required check; use name@app-id")
				}
				required = append(required, release.RequiredCheck{Name: name, AppID: id})
			}
			token := os.Getenv("SOFA_GATE_READ_TOKEN")
			var client *github.Client
			if token == "" {
				client = github.NewAnonymous(nil)
			} else {
				var err error
				client, err = github.New(token, nil)
				if err != nil {
					return err
				}
			}
			ctx := cmd.Context()
			pull, err := client.Pull(ctx, repo, number)
			if err != nil {
				return err
			}
			info, err := client.RepositoryInfo(ctx, repo)
			if err != nil || !strings.EqualFold(info.FullName, repo) || info.DefaultBranch == "" {
				return errors.New("designated repository unavailable")
			}
			defaultHead, exists, err := client.Reference(ctx, repo, info.DefaultBranch)
			if err != nil || !exists {
				return errors.New("designated default branch unavailable")
			}
			if pull.BaseRef != info.DefaultBranch {
				return errors.New("designated PR does not target default branch")
			}
			onDefault := false
			var observedChecks []github.CommitCheck
			if pull.Merged {
				onDefault, err = client.IsAncestor(ctx, repo, pull.MergeCommitSHA, defaultHead)
				if err != nil {
					return err
				}
				if onDefault {
					observedChecks, err = client.CommitChecks(ctx, repo, pull.MergeCommitSHA)
					if err != nil {
						return err
					}
				}
			}
			decision, err := release.EvaluateDesignated(repo, pull, mergeSHA, defaultHead, onDefault, required, observedChecks)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(struct {
				Repository  string `json:"repository"`
				PRNumber    int64  `json:"pr_number"`
				Outcome     string `json:"outcome"`
				CommitSHA   string `json:"commit_sha"`
				DefaultHead string `json:"default_head"`
				EvidenceRef string `json:"evidence_ref"`
			}{
				Repository:  repo,
				PRNumber:    number,
				Outcome:     decision.Kind,
				CommitSHA:   decision.CommitSHA,
				DefaultHead: defaultHead,
				EvidenceRef: decision.EvidenceRef,
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repository", "", "Exact owner/repository")
	cmd.Flags().Int64Var(&number, "pr", 0, "Designated pull request number")
	cmd.Flags().StringVar(&mergeSHA, "expected-merge-sha", "", "Known merge commit SHA for merged PRs")
	cmd.Flags().StringArrayVar(&checks, "required-check", nil, "Required check name@app-id; repeat per check")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error { return fmt.Errorf("invalid release-observe flags") })
	return cmd
}

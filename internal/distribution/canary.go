package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/kevinmartin/sofa/internal/github"
)

const canaryMaxAge = 24 * time.Hour

type Canary struct {
	ReleaseVersion    string `json:"release_version"`
	SourceSHA         string `json:"source_sha"`
	Correlation       string `json:"correlation"`
	CallerSHA         string `json:"caller_sha"`
	ReleaseRunID      string `json:"release_run_id"`
	ReleaseRunAttempt string `json:"release_run_attempt"`
	RunID             int64  `json:"run_id"`
}

func ValidateCanary(run github.ReleaseCanaryRun, p Plan, evidence Canary) error {
	if err := p.Validate(); err != nil {
		return err
	}
	correlation, err := Correlation(p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt)
	if err != nil || correlation != evidence.Correlation {
		return errors.New("release canary correlation does not bind this release")
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() || run.CreatedAt.Before(now.Add(-canaryMaxAge)) || run.CreatedAt.After(now.Add(time.Minute)) {
		return errors.New("release canary creation time invalid")
	}
	if evidence.ReleaseVersion != p.Version || evidence.SourceSHA != p.SourceSHA || !shaPattern.MatchString(evidence.Correlation) || !shaPattern.MatchString(evidence.CallerSHA) || evidence.RunID < 1 || run.ID != evidence.RunID || run.Title != "Sofa release canary / "+evidence.Correlation || run.Event != "workflow_dispatch" || run.Path != ".github/workflows/sofa-release-canary.yml" || run.HeadSHA != evidence.CallerSHA || run.Repository.ID < 1 || run.Repository.ID != run.HeadRepository.ID || run.Repository.FullName != DisposableRepository || run.HeadRepository.FullName != DisposableRepository || run.Status != "completed" || run.Conclusion != "success" {
		return errors.New("release canary has no matching successful trusted run")
	}
	return nil
}

// Correlation binds a dispatch to the exact release/source and Actions attempt.
func Correlation(p Plan, runID, attempt string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if id, err := strconv.ParseInt(runID, 10, 64); err != nil || id < 1 {
		return "", errors.New("release run identity invalid")
	}
	if n, err := strconv.Atoi(attempt); err != nil || n < 1 {
		return "", errors.New("release attempt identity invalid")
	}
	sum := sha256.Sum256([]byte(p.Version + "\n" + p.SourceSHA + "\n" + runID + "\n" + attempt))
	return hex.EncodeToString(sum[:20]), nil
}

type CanaryAPI interface {
	RepositoryInfo(context.Context, string) (github.RepositoryInfo, error)
	Reference(context.Context, string, string) (string, bool, error)
	DispatchReleaseCanary(context.Context, string, string, string, string, string, string, string) error
	ReleaseCanaryRuns(context.Context, string, int, time.Time) ([]github.ReleaseCanaryRun, error)
	ReleaseCanaryRun(context.Context, string, int64) (github.ReleaseCanaryRun, error)
}

// RunCanary correlates before and after dispatch, so retrying an interrupted
// polling operation finds its existing run rather than triggering a new one.
func RunCanary(ctx context.Context, api CanaryAPI, p Plan, releaseRunID, releaseRunAttempt string, poll time.Duration) (Canary, error) {
	// An Actions attempt cannot outlive this window. Its ten-minute observer
	// retries therefore find the same correlation without scanning lifetime
	// history. A rerun has a distinct attempt and correlation.
	since := time.Now().UTC().Add(-canaryMaxAge)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Canary{}, err
	}
	if err := p.Validate(); err != nil {
		return Canary{}, err
	}
	correlation, err := Correlation(p, releaseRunID, releaseRunAttempt)
	if err != nil || poll <= 0 {
		return Canary{}, errors.New("invalid release canary correlation")
	}
	info, err := api.RepositoryInfo(ctx, DisposableRepository)
	if err != nil {
		return Canary{}, err
	}
	if info.FullName != DisposableRepository || info.DefaultBranch == "" || info.Archived || info.Disabled {
		return Canary{}, errors.New("release canary repository unavailable")
	}
	caller, found, err := api.Reference(ctx, DisposableRepository, info.DefaultBranch)
	if err != nil {
		return Canary{}, err
	}
	if !found || !shaPattern.MatchString(caller) {
		return Canary{}, errors.New("release canary caller unavailable")
	}
	evidence := Canary{
		ReleaseVersion:    p.Version,
		SourceSHA:         p.SourceSHA,
		Correlation:       correlation,
		CallerSHA:         caller,
		ReleaseRunID:      releaseRunID,
		ReleaseRunAttempt: releaseRunAttempt,
	}
	find := func() (github.ReleaseCanaryRun, bool, error) {
		var match github.ReleaseCanaryRun
		for page := 1; page <= 10; page++ {
			runs, err := api.ReleaseCanaryRuns(ctx, DisposableRepository, page, since)
			if err != nil {
				return github.ReleaseCanaryRun{}, false, err
			}
			for _, run := range runs {
				if run.Title != "Sofa release canary / "+correlation {
					continue
				}
				if run.CreatedAt.IsZero() || run.CreatedAt.Before(since) || run.CreatedAt.After(time.Now().UTC().Add(time.Minute)) {
					return github.ReleaseCanaryRun{}, false, errors.New("release canary creation time invalid")
				}
				if match.ID != 0 && match.ID != run.ID {
					return github.ReleaseCanaryRun{}, false, errors.New("duplicate release canary correlation")
				}
				match = run
			}
			if len(runs) < 100 {
				return match, match.ID > 0, nil
			}
		}
		return github.ReleaseCanaryRun{}, false, errors.New("release canary discovery exceeds bound")
	}
	run, found, err := find()
	if err != nil {
		return Canary{}, err
	}
	if !found {
		if err := api.DispatchReleaseCanary(ctx, DisposableRepository, info.DefaultBranch, p.Version, p.SourceSHA, correlation, releaseRunID, releaseRunAttempt); err != nil {
			return Canary{}, err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return Canary{}, err
		}
		if found {
			if run.CreatedAt.Before(since) {
				return Canary{}, errors.New("release canary creation time outside attempt window")
			}
			evidence.RunID = run.ID
			if run.Status == "completed" {
				return evidence, ValidateCanary(run, p, evidence)
			}
			// Fail on wrong identity before trusting a queued run or polling it.
			candidate := run
			candidate.Status = "completed"
			candidate.Conclusion = "success"
			if err := ValidateCanary(candidate, p, evidence); err != nil {
				return Canary{}, err
			}
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Canary{}, ctx.Err()
		case <-timer.C:
		}
		if found {
			run, err = api.ReleaseCanaryRun(ctx, DisposableRepository, run.ID)
		} else {
			run, found, err = find()
		}
		if err != nil {
			return Canary{}, err
		}
	}
}

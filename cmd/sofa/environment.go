package main

import (
	"errors"
	"os"
	"strconv"

	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

func clientFromEnv(name string) (*github.Client, error) {
	return github.New(os.Getenv(name), nil)
}

func ownerFromEnv() (state.Owner, error) {
	attempt, err := strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil || attempt < 1 || os.Getenv("GITHUB_RUN_ID") == "" {
		return state.Owner{}, errors.New("Actions run identity unavailable")
	}
	return state.Owner{
		RunID:      os.Getenv("GITHUB_RUN_ID"),
		RunAttempt: attempt,
	}, nil
}

func forbidPrivilegedEnv(modelEnv string, includeModel bool) error {
	names := []string{"SOFA_PROJECTS_TOKEN", "SOFA_STATE_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_APP_PRIVATE_KEY"}
	if includeModel {
		names = append(names, modelEnv, "GITHUB_TOKEN", "GH_TOKEN")
	}
	for _, name := range names {
		if os.Getenv(name) != "" {
			return errors.New("privileged credential present in secretless stage")
		}
	}
	return nil
}

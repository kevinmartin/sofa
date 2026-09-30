package main

import (
	"errors"

	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/discovery"
)

func discoveryPolicy(c config.Config) (discovery.Policy, error) {
	if c.Lifecycle == nil {
		return discovery.Policy{}, errors.New("lifecycle configuration is unavailable")
	}
	status := c.Lifecycle.Statuses
	return discovery.Policy{
		Repository:       c.Repository,
		RepositoryID:     c.RepositoryID,
		ProjectID:        c.ProjectID,
		DiscoveryStatus:  status["discovery"],
		SpecReviewStatus: status["spec_review"],
		BacklogStatus:    status["backlog"],
		ReadyStatus:      status["ready"],
	}, nil
}

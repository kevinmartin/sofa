package github

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
)

const issueQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){id nameWithOwner defaultBranchRef{target{oid}} issue(number:$number){id number title body state lastEditedAt}}}`
const projectsQuery = `query($id:ID!,$after:String){node(id:$id){... on Issue{projectItems(first:100,after:$after){nodes{id isArchived project{id public} fieldValueByName(name:"Status"){... on ProjectV2ItemFieldSingleSelectValue{name optionId updatedAt}}} pageInfo{hasNextPage endCursor}}}}}`

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// Issue reads every relevant connection. A bounded pagination limit fails closed
// rather than returning a partial history as authorization.
func (c *Client) Issue(ctx context.Context, policy config.Config, number int) (admission.Snapshot, error) {
	var s admission.Snapshot
	if err := policy.Validate(); err != nil {
		return s, err
	}
	if number < 1 {
		return s, errors.New("issue number must be positive")
	}
	parts := strings.Split(policy.Repository, "/")
	var data struct {
		Repository *struct {
			ID               string
			NameWithOwner    string
			DefaultBranchRef *struct{ Target struct{ OID string } }
			Issue            *struct {
				ID                 string
				Number             int
				Title, Body, State string
				LastEditedAt       *time.Time
			}
		}
	}
	if err := c.GraphQL(ctx, issueQuery, map[string]any{"owner": parts[0], "name": parts[1], "number": number}, &data); err != nil {
		return s, err
	}
	r := data.Repository
	if r == nil || r.Issue == nil || r.DefaultBranchRef == nil || r.ID != policy.RepositoryID || !strings.EqualFold(r.NameWithOwner, policy.Repository) {
		return s, errors.New("configured repository or issue unavailable")
	}
	s = admission.Snapshot{Repository: policy.Repository, RepositoryID: r.ID, IssueID: r.Issue.ID, Number: r.Issue.Number, Title: r.Issue.Title, Body: r.Issue.Body, Open: r.Issue.State == "OPEN", BaseSHA: r.DefaultBranchRef.Target.OID}
	if r.Issue.LastEditedAt != nil {
		s.IssueLastEditedAt = *r.Issue.LastEditedAt
	}
	if _, _, err := admission.CanonicalSpec(s.Title, s.Body); err != nil {
		return admission.Snapshot{}, err
	}
	status, err := c.projectStatus(ctx, s.IssueID, policy.ProjectID)
	if err != nil {
		return admission.Snapshot{}, err
	}
	s.ProjectID = policy.ProjectID
	s.ProjectPrivate = status.Private
	s.ProjectItemID = status.ItemID
	s.CurrentStatus = status.Name
	s.StatusOptionID = status.OptionID
	s.StatusUpdatedAt = status.UpdatedAt
	s.Complete = true
	return s, nil
}

func paginate(ctx context.Context, fetch func(any) (pageInfo, error)) error {
	var cursor any
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := fetch(cursor)
		if err != nil {
			return err
		}
		if !p.HasNextPage {
			return nil
		}
		if p.EndCursor == "" || seen[p.EndCursor] {
			return errors.New("invalid GitHub pagination")
		}
		seen[p.EndCursor] = true
		cursor = p.EndCursor
	}
	return errors.New("GitHub history exceeds bounded pagination; authorization unavailable")
}

type projectStatus struct {
	ItemID    string
	Private   bool
	Name      string
	OptionID  string
	UpdatedAt time.Time
}

func (c *Client) projectStatus(ctx context.Context, id, projectID string) (projectStatus, error) {
	var status projectStatus
	matches := 0
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				ProjectItems struct {
					Nodes []struct {
						ID         string
						IsArchived bool
						Project    struct {
							ID     string
							Public *bool
						}
						FieldValueByName *struct {
							Name, OptionID string
							UpdatedAt      time.Time
						}
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.GraphQL(ctx, projectsQuery, map[string]any{"id": id, "after": cursor}, &data); err != nil {
			return pageInfo{}, err
		}
		if data.Node == nil {
			return pageInfo{}, errors.New("Project items unavailable")
		}
		for _, n := range data.Node.ProjectItems.Nodes {
			if n.Project.ID == projectID && !n.IsArchived {
				if n.Project.Public == nil {
					return pageInfo{}, errors.New("Project visibility unavailable")
				}
				matches++
				if n.FieldValueByName != nil {
					status = projectStatus{ItemID: n.ID, Private: !*n.Project.Public, Name: n.FieldValueByName.Name, OptionID: n.FieldValueByName.OptionID, UpdatedAt: n.FieldValueByName.UpdatedAt}
				}
			}
		}
		return data.Node.ProjectItems.PageInfo, nil
	})
	if err != nil {
		return projectStatus{}, err
	}
	if matches != 1 || status.ItemID == "" || status.Name == "" || status.OptionID == "" || status.UpdatedAt.IsZero() || !status.Private {
		return projectStatus{}, errors.New("unique active private Project status unavailable")
	}
	return status, nil
}

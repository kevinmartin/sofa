package github

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
)

const issueQuery = `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){id nameWithOwner defaultBranchRef{target{oid}} issue(number:$number){id number title body state}}}`
const commentsQuery = `query($id:ID!,$after:String){node(id:$id){... on Issue{comments(first:100,after:$after){nodes{id author{... on User{id}} body createdAt updatedAt} pageInfo{hasNextPage endCursor}}}}}`
const eventsQuery = `query($id:ID!,$after:String){node(id:$id){... on Issue{timelineItems(first:100,after:$after,itemTypes:[PROJECT_V2_ITEM_STATUS_CHANGED_EVENT]){nodes{... on ProjectV2ItemStatusChangedEvent{id actor{... on User{id}} project{id} status wasAutomated createdAt}} pageInfo{hasNextPage endCursor}}}}}`
const projectsQuery = `query($id:ID!,$after:String){node(id:$id){... on Issue{projectItems(first:100,after:$after){nodes{isArchived project{id} fieldValueByName(name:"Status"){... on ProjectV2ItemFieldSingleSelectValue{name}}} pageInfo{hasNextPage endCursor}}}}}`

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}
type nodeID struct {
	ID string `json:"id"`
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
			}
		}
	}
	if err := c.GraphQL(ctx, issueQuery, map[string]any{"owner": parts[0], "name": parts[1], "number": number}, &data); err != nil {
		return s, err
	}
	r := data.Repository
	if r == nil || r.Issue == nil || r.DefaultBranchRef == nil || r.ID != policy.RepositoryID || r.NameWithOwner != policy.Repository {
		return s, errors.New("configured repository or issue unavailable")
	}
	s = admission.Snapshot{Repository: r.NameWithOwner, RepositoryID: r.ID, IssueID: r.Issue.ID, Number: r.Issue.Number, Title: r.Issue.Title, Body: r.Issue.Body, Open: r.Issue.State == "OPEN", BaseSHA: r.DefaultBranchRef.Target.OID}
	if _, _, err := admission.CanonicalSpec(s.Title, s.Body); err != nil {
		return admission.Snapshot{}, err
	}
	comments, err := c.comments(ctx, s.IssueID)
	if err != nil {
		return admission.Snapshot{}, err
	}
	s.Comments = comments
	events, err := c.events(ctx, s.IssueID)
	if err != nil {
		return admission.Snapshot{}, err
	}
	s.StatusEvents = events
	status, err := c.projectStatus(ctx, s.IssueID, policy.ProjectID)
	if err != nil {
		return admission.Snapshot{}, err
	}
	s.ProjectID = policy.ProjectID
	s.CurrentStatus = status
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

func (c *Client) comments(ctx context.Context, id string) ([]admission.Comment, error) {
	var out []admission.Comment
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				Comments struct {
					Nodes []struct {
						ID, Body             string
						Author               *nodeID
						CreatedAt, UpdatedAt time.Time
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.GraphQL(ctx, commentsQuery, map[string]any{"id": id, "after": cursor}, &data); err != nil {
			return pageInfo{}, err
		}
		if data.Node == nil {
			return pageInfo{}, errors.New("issue comments unavailable")
		}
		for _, n := range data.Node.Comments.Nodes {
			actor := ""
			if n.Author != nil {
				actor = n.Author.ID
			}
			if len(n.Body) > 64<<10 {
				return pageInfo{}, errors.New("comment exceeds input limit")
			}
			out = append(out, admission.Comment{ID: n.ID, ActorID: actor, Body: n.Body, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt})
		}
		return data.Node.Comments.PageInfo, nil
	})
	return out, err
}

func (c *Client) events(ctx context.Context, id string) ([]admission.StatusEvent, error) {
	var out []admission.StatusEvent
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				TimelineItems struct {
					Nodes []struct {
						ID, Status     string
						Actor, Project *nodeID
						CreatedAt      time.Time
						WasAutomated   bool
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.GraphQL(ctx, eventsQuery, map[string]any{"id": id, "after": cursor}, &data); err != nil {
			return pageInfo{}, err
		}
		if data.Node == nil {
			return pageInfo{}, errors.New("issue status history unavailable")
		}
		for _, n := range data.Node.TimelineItems.Nodes {
			if n.ID == "" {
				return pageInfo{}, errors.New("status history contains unavailable event")
			}
			actor, project := "", ""
			if n.Actor != nil {
				actor = n.Actor.ID
			}
			if n.Project != nil {
				project = n.Project.ID
			}
			out = append(out, admission.StatusEvent{ID: n.ID, ActorID: actor, ProjectID: project, Status: n.Status, WasAutomated: n.WasAutomated, CreatedAt: n.CreatedAt})
		}
		return data.Node.TimelineItems.PageInfo, nil
	})
	return out, err
}

func (c *Client) projectStatus(ctx context.Context, id, projectID string) (string, error) {
	status := ""
	matches := 0
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				ProjectItems struct {
					Nodes []struct {
						IsArchived       bool
						Project          nodeID
						FieldValueByName *struct{ Name string }
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
				matches++
				if n.FieldValueByName != nil {
					status = n.FieldValueByName.Name
				}
			}
		}
		return data.Node.ProjectItems.PageInfo, nil
	})
	if err != nil {
		return "", err
	}
	if matches != 1 || status == "" {
		return "", errors.New("unique active Project status unavailable")
	}
	return status, nil
}

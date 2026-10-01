package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/lifecycle"
)

type ProjectWorkItem struct {
	Issue             admission.Snapshot
	BlockedReason     string
	NextAction        string
	Dependencies      []int
	DependenciesKnown bool
	Priority          string
	PriorityRank      int // P0 is highest; P4 is lowest.
	PriorityKnown     bool
	MetadataError     string
}

// ProjectIssues is the metadata-only scan used by lifecycle polling. It does
// not read optional owner-managed fields unless the richer API is requested.
func (c *Client) ProjectIssues(ctx context.Context, policy config.Config) ([]admission.Snapshot, error) {
	items, err := c.projectWorkItems(ctx, policy, "", "", "", "")
	if err != nil {
		return nil, err
	}
	issues := make([]admission.Snapshot, len(items))
	for i, item := range items {
		issues[i] = item.Issue
	}
	return issues, nil
}

// ProjectWorkItems reads optional owner-managed dependency and priority fields.
// A missing configured value remains unknown and must block related admission.
func (c *Client) ProjectWorkItems(ctx context.Context, policy config.Config) ([]ProjectWorkItem, error) {
	if policy.Lifecycle == nil {
		return c.projectWorkItems(ctx, policy, "", "", "", "")
	}
	blockedField, nextField := policy.Lifecycle.EffectiveAdviceFields()
	return c.projectWorkItems(ctx, policy, policy.Lifecycle.DependenciesField, policy.Lifecycle.PriorityField, blockedField, nextField)
}

// projectWorkItems lists only issues from the configured repository in the
// configured private Project. A partial page or ambiguous issue identity is
// an error: callers must never use an incomplete poll to authorize work.
func (c *Client) projectWorkItems(ctx context.Context, policy config.Config, dependencyField, priorityField, blockedField, nextField string) ([]ProjectWorkItem, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	const query = `query($id:ID!,$after:String,$dependencyName:String!,$priorityName:String!,$blockedName:String!,$nextName:String!,$hasDependencies:Boolean!,$hasPriority:Boolean!,$hasAdvice:Boolean!){node(id:$id){... on ProjectV2{id public items(first:100,after:$after){nodes{id isArchived content{... on Issue{id number title body state lastEditedAt repository{id nameWithOwner defaultBranchRef{target{oid}}}}} fieldValueByName(name:"Status"){... on ProjectV2ItemFieldSingleSelectValue{name optionId updatedAt}} dependencies:fieldValueByName(name:$dependencyName) @include(if:$hasDependencies){... on ProjectV2ItemFieldTextValue{text}} priority:fieldValueByName(name:$priorityName) @include(if:$hasPriority){... on ProjectV2ItemFieldSingleSelectValue{name optionId} ... on ProjectV2ItemFieldTextValue{text}} blockedReason:fieldValueByName(name:$blockedName) @include(if:$hasAdvice){... on ProjectV2ItemFieldTextValue{text}} nextAction:fieldValueByName(name:$nextName) @include(if:$hasAdvice){... on ProjectV2ItemFieldTextValue{text}}} pageInfo{hasNextPage endCursor}}}}}`
	items := make([]ProjectWorkItem, 0)
	seen := make(map[string]bool)
	seenItems := make(map[string]bool)
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				ID     string
				Public *bool
				Items  struct {
					Nodes []struct {
						ID         string
						IsArchived bool
						Content    *struct {
							ID           string
							Number       int
							Title        string
							Body         string
							State        string
							LastEditedAt *time.Time
							Repository   *struct {
								ID               string
								NameWithOwner    string
								DefaultBranchRef *struct{ Target struct{ OID string } }
							}
						}
						FieldValueByName *struct {
							Name      string
							OptionID  string
							UpdatedAt time.Time
						}
						Dependencies *struct{ Text string }
						Priority     *struct {
							Name string
							Text string
						}
						BlockedReason *struct{ Text string }
						NextAction    *struct{ Text string }
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.GraphQL(ctx, query, map[string]any{"id": policy.ProjectID, "after": cursor, "dependencyName": dependencyField, "priorityName": priorityField, "blockedName": blockedField, "nextName": nextField, "hasDependencies": dependencyField != "", "hasPriority": priorityField != "", "hasAdvice": blockedField != "" && nextField != ""}, &data); err != nil {
			return pageInfo{}, err
		}
		if data.Node == nil || data.Node.ID != policy.ProjectID || data.Node.Public == nil || *data.Node.Public {
			return pageInfo{}, errors.New("configured private Project unavailable")
		}
		for _, item := range data.Node.Items.Nodes {
			if item.IsArchived || item.Content == nil || item.Content.Repository == nil || item.Content.Repository.ID != policy.RepositoryID || !strings.EqualFold(item.Content.Repository.NameWithOwner, policy.Repository) {
				continue
			}
			if item.ID == "" || item.Content.ID == "" || item.Content.Number <= 0 || item.Content.Repository.DefaultBranchRef == nil || seen[item.Content.ID] || seenItems[item.ID] {
				return pageInfo{}, errors.New("project issue identity unavailable")
			}
			seen[item.Content.ID] = true
			seenItems[item.ID] = true
			// An unset Status grants no stage or authority. Keep scanning other
			// issues, while retaining duplicate detection for this item.
			if item.FieldValueByName == nil || item.FieldValueByName.Name == "" || item.FieldValueByName.OptionID == "" || item.FieldValueByName.UpdatedAt.IsZero() {
				continue
			}
			snapshot := admission.Snapshot{
				Repository:      policy.Repository,
				RepositoryID:    policy.RepositoryID,
				IssueID:         item.Content.ID,
				Number:          item.Content.Number,
				Title:           item.Content.Title,
				Body:            item.Content.Body,
				Open:            item.Content.State == "OPEN",
				ProjectID:       policy.ProjectID,
				ProjectPrivate:  true,
				ProjectItemID:   item.ID,
				CurrentStatus:   item.FieldValueByName.Name,
				StatusOptionID:  item.FieldValueByName.OptionID,
				StatusUpdatedAt: item.FieldValueByName.UpdatedAt,
				BaseSHA:         item.Content.Repository.DefaultBranchRef.Target.OID,
				Complete:        true,
			}
			if item.Content.LastEditedAt != nil {
				snapshot.IssueLastEditedAt = *item.Content.LastEditedAt
			}
			workItem := ProjectWorkItem{
				Issue: snapshot,
			}
			if item.BlockedReason != nil {
				workItem.BlockedReason = item.BlockedReason.Text
			}
			if item.NextAction != nil {
				workItem.NextAction = item.NextAction.Text
			}
			if dependencyField == "" {
				workItem.DependenciesKnown = true
			} else if item.Dependencies != nil {
				deps, parseErr := ParseDependencies(item.Dependencies.Text, snapshot.Number)
				if parseErr != nil {
					workItem.MetadataError = "invalid owner-managed dependencies field"
				} else {
					workItem.Dependencies = deps
					workItem.DependenciesKnown = true
				}
			}
			if priorityField == "" {
				workItem.PriorityKnown = true
			} else if item.Priority != nil {
				workItem.Priority = item.Priority.Name
				if workItem.Priority == "" {
					workItem.Priority = item.Priority.Text
				}
				if priorityPattern.MatchString(workItem.Priority) {
					workItem.PriorityKnown = true
					workItem.PriorityRank = int(workItem.Priority[1] - '0')
				} else if workItem.Priority != "" {
					workItem.Priority = ""
					workItem.MetadataError = "invalid owner-managed priority field"
				}
			}
			items = append(items, workItem)
		}
		return data.Node.Items.PageInfo, nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Issue.Number < items[j].Issue.Number })
	return items, nil
}

var dependencyPattern = regexp.MustCompile(`^#[1-9][0-9]*(, #[1-9][0-9]*)*$`)
var priorityPattern = regexp.MustCompile(`^P[0-4]$`)

// ParseDependencies accepts only explicit same-repository issue references in
// a restricted Project text field. "none" is an intentional empty value;
// blank/malformed data cannot silently become dependency-free.
func ParseDependencies(text string, self int) ([]int, error) {
	if text == "none" {
		return []int{}, nil
	}
	if len(text) > 2048 || !dependencyPattern.MatchString(text) {
		return nil, errors.New("invalid owner-managed dependencies field")
	}
	var numbers []int
	seen := make(map[int]bool)
	for _, entry := range strings.Split(text, ", ") {
		number, err := strconv.Atoi(strings.TrimPrefix(entry, "#"))
		if err != nil || number <= 0 || number == self || seen[number] {
			return nil, errors.New("invalid owner-managed dependency issue")
		}
		seen[number] = true
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	return numbers, nil
}

// ProjectStatusField resolves the trusted Project's built-in Status field.
// Names are display labels; option IDs are what status mutations must use.
func (c *Client) ProjectStatusField(ctx context.Context, projectID string) (string, map[string]string, error) {
	if projectID == "" {
		return "", nil, errors.New("project identity required")
	}
	const query = `query($id:ID!,$after:String){node(id:$id){... on ProjectV2{id public fields(first:100,after:$after){nodes{... on ProjectV2SingleSelectField{id name options{id name}}} pageInfo{hasNextPage endCursor}}}}}`
	var fieldID string
	options := make(map[string]string)
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				ID     string
				Public *bool
				Fields struct {
					Nodes []struct {
						ID      string
						Name    string
						Options []struct{ ID, Name string }
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.GraphQL(ctx, query, map[string]any{"id": projectID, "after": cursor}, &data); err != nil {
			return pageInfo{}, err
		}
		if data.Node == nil || data.Node.ID != projectID || data.Node.Public == nil || *data.Node.Public {
			return pageInfo{}, errors.New("configured private Project unavailable")
		}
		for _, field := range data.Node.Fields.Nodes {
			if field.Name != "Status" {
				continue
			}
			if fieldID != "" || field.ID == "" {
				return pageInfo{}, errors.New("ambiguous Project Status field")
			}
			fieldID = field.ID
			for _, option := range field.Options {
				if option.ID == "" || option.Name == "" || options[option.Name] != "" {
					return pageInfo{}, errors.New("ambiguous Project Status option")
				}
				options[option.Name] = option.ID
			}
		}
		return data.Node.Fields.PageInfo, nil
	})
	if err != nil {
		return "", nil, err
	}
	if fieldID == "" || len(options) == 0 {
		return "", nil, errors.New("project Status field unavailable")
	}
	return fieldID, options, nil
}

// SetProjectStatusIfCurrent re-reads the item's trusted status immediately
// before a mutation. GraphQL has no conditional field update, so callers must
// also re-poll after writing and treat concurrent manual moves as conflicts.
func (c *Client) SetProjectStatusIfCurrent(ctx context.Context, issueID, projectID, itemID, currentOptionID string, currentUpdatedAt time.Time, statuses lifecycle.Statuses, targetStage lifecycle.Stage) error {
	switch targetStage {
	case lifecycle.SpecReview, lifecycle.Building, lifecycle.Verification, lifecycle.Review, lifecycle.Release, lifecycle.Done:
	default:
		return errors.New("project status requires owner transition")
	}
	if issueID == "" || projectID == "" || itemID == "" || currentOptionID == "" || currentUpdatedAt.IsZero() || statuses.Validate() != nil {
		return errors.New("project status identity unavailable")
	}
	fieldID, options, err := c.ProjectStatusField(ctx, projectID)
	if err != nil {
		return err
	}
	targetOptionID := options[statuses[targetStage]]
	if targetOptionID == "" {
		return errors.New("target Project status option unavailable")
	}
	current, err := c.projectStatus(ctx, issueID, projectID)
	if err != nil {
		return err
	}
	if current.ItemID != itemID || current.OptionID != currentOptionID || !current.UpdatedAt.Equal(currentUpdatedAt) {
		return errors.New("project status changed during reconciliation")
	}
	if currentOptionID == targetOptionID {
		return nil
	}
	const mutation = `mutation($project:ID!,$item:ID!,$field:ID!,$option:String!){updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{singleSelectOptionId:$option}}){projectV2Item{id}}}`
	var data struct {
		UpdateProjectV2ItemFieldValue *struct{ ProjectV2Item *struct{ ID string } }
	}
	if err := c.GraphQL(ctx, mutation, map[string]any{"project": projectID, "item": itemID, "field": fieldID, "option": targetOptionID}, &data); err != nil {
		return err
	}
	if data.UpdateProjectV2ItemFieldValue == nil || data.UpdateProjectV2ItemFieldValue.ProjectV2Item == nil || data.UpdateProjectV2ItemFieldValue.ProjectV2Item.ID != itemID {
		return fmt.Errorf("project status update identity unavailable")
	}
	return nil
}

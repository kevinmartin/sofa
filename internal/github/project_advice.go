package github

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
)

// ProjectAdviceFields identifies two distinct text fields in one private Project.
// Resolve these once per poll before treating an empty value as a cleared hold.
type ProjectAdviceFields struct {
	BlockedReasonID string
	NextActionID    string
	BlockedName     string
	NextName        string
}

// ProjectAdviceFields resolves display names to text field IDs. When optional
// defaults are absent as a pair, existing Projects keep their prior behavior;
// a partial pair or explicitly configured missing field is a setup error.
func (c *Client) ProjectAdviceFields(ctx context.Context, projectID, blockedName, nextName string, required bool) (ProjectAdviceFields, error) {
	var fields ProjectAdviceFields
	if !validAdviceIdentity(projectID) || !validAdviceName(blockedName) || !validAdviceName(nextName) || blockedName == nextName || blockedName == "Status" || nextName == "Status" {
		return fields, errors.New("invalid Project advice field configuration")
	}
	fields.BlockedName = blockedName
	fields.NextName = nextName
	const query = `query($id:ID!,$after:String){node(id:$id){... on ProjectV2{id public fields(first:100,after:$after){nodes{... on ProjectV2FieldCommon{id name dataType}} pageInfo{hasNextPage endCursor}}}}}`
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				ID     string
				Public *bool
				Fields struct {
					Nodes []struct {
						ID       string
						Name     string
						DataType string
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
			if field.Name != blockedName && field.Name != nextName {
				continue
			}
			if field.DataType != "TEXT" || !validAdviceIdentity(field.ID) {
				return pageInfo{}, errors.New("project advice text field unavailable")
			}
			if field.Name == blockedName {
				if fields.BlockedReasonID != "" || field.ID == fields.NextActionID {
					return pageInfo{}, errors.New("ambiguous Project advice field")
				}
				fields.BlockedReasonID = field.ID
			} else {
				if fields.NextActionID != "" || field.ID == fields.BlockedReasonID {
					return pageInfo{}, errors.New("ambiguous Project advice field")
				}
				fields.NextActionID = field.ID
			}
		}
		return data.Node.Fields.PageInfo, nil
	})
	if err != nil {
		return ProjectAdviceFields{}, err
	}
	if fields.BlockedReasonID == "" && fields.NextActionID == "" && !required {
		return ProjectAdviceFields{}, nil
	}
	if fields.BlockedReasonID == "" || fields.NextActionID == "" {
		return ProjectAdviceFields{}, errors.New("project advice text fields unavailable")
	}
	return fields, nil
}

type projectAdviceSnapshot struct {
	ItemID        string
	OptionID      string
	UpdatedAt     time.Time
	BlockedReason string
	NextAction    string
}

func (c *Client) projectAdvice(ctx context.Context, issueID, projectID string, fields ProjectAdviceFields) (projectAdviceSnapshot, error) {
	var found projectAdviceSnapshot
	const query = `query($id:ID!,$after:String,$blockedName:String!,$nextName:String!){node(id:$id){... on Issue{projectItems(first:100,after:$after){nodes{id isArchived project{id public} fieldValueByName(name:"Status"){... on ProjectV2ItemFieldSingleSelectValue{optionId updatedAt}} blockedReason:fieldValueByName(name:$blockedName){... on ProjectV2ItemFieldTextValue{text}} nextAction:fieldValueByName(name:$nextName){... on ProjectV2ItemFieldTextValue{text}}} pageInfo{hasNextPage endCursor}}}}}`
	err := paginate(ctx, func(cursor any) (pageInfo, error) {
		var data struct {
			Node *struct {
				ProjectItems struct {
					Nodes []struct {
						ID         string
						IsArchived bool
						Project    *struct {
							ID     string
							Public *bool
						}
						FieldValueByName *struct {
							OptionID  string
							UpdatedAt time.Time
						}
						BlockedReason *struct{ Text string }
						NextAction    *struct{ Text string }
					}
					PageInfo pageInfo
				}
			}
		}
		if err := c.GraphQL(ctx, query, map[string]any{"id": issueID, "after": cursor, "blockedName": fields.BlockedName, "nextName": fields.NextName}, &data); err != nil {
			return pageInfo{}, err
		}
		if data.Node == nil {
			return pageInfo{}, errors.New("project advice issue unavailable")
		}
		for _, item := range data.Node.ProjectItems.Nodes {
			if item.Project == nil || item.Project.ID != projectID || item.IsArchived {
				continue
			}
			if found.ItemID != "" || item.Project.Public == nil || *item.Project.Public || !validAdviceIdentity(item.ID) || item.FieldValueByName == nil || item.FieldValueByName.OptionID == "" || item.FieldValueByName.UpdatedAt.IsZero() {
				return pageInfo{}, errors.New("project advice item identity unavailable")
			}
			found.ItemID = item.ID
			found.OptionID = item.FieldValueByName.OptionID
			found.UpdatedAt = item.FieldValueByName.UpdatedAt
			if item.BlockedReason != nil {
				found.BlockedReason = item.BlockedReason.Text
			}
			if item.NextAction != nil {
				found.NextAction = item.NextAction.Text
			}
		}
		return data.Node.ProjectItems.PageInfo, nil
	})
	if err != nil {
		return projectAdviceSnapshot{}, err
	}
	if found.ItemID == "" {
		return projectAdviceSnapshot{}, errors.New("project advice item unavailable")
	}
	return found, nil
}

// SetProjectAdviceIfCurrent writes controller-owned text without changing Status.
// A fresh exact status check precedes each changed field. GitHub has no
// conditional field mutation, so a manual move in the final read/write gap
// remains a Project API race, as with Status updates.
func (c *Client) SetProjectAdviceIfCurrent(ctx context.Context, issue admission.Snapshot, fields ProjectAdviceFields, blockedReason, nextAction string) error {
	if !issue.Complete || !issue.ProjectPrivate || !validAdviceIdentity(issue.IssueID) || !validAdviceIdentity(issue.ProjectID) || !validAdviceIdentity(issue.ProjectItemID) || !validAdviceIdentity(issue.StatusOptionID) || issue.StatusUpdatedAt.IsZero() || !validAdviceIdentity(fields.BlockedReasonID) || !validAdviceIdentity(fields.NextActionID) || fields.BlockedReasonID == fields.NextActionID || !validAdviceName(fields.BlockedName) || !validAdviceName(fields.NextName) || fields.BlockedName == fields.NextName || !validAdviceText(blockedReason) || !validAdviceText(nextAction) {
		return errors.New("invalid Project advice identity or text")
	}
	updates := []struct {
		id   string
		want string
		read func(projectAdviceSnapshot) string
	}{
		{id: fields.BlockedReasonID, want: blockedReason, read: func(s projectAdviceSnapshot) string { return s.BlockedReason }},
		{id: fields.NextActionID, want: nextAction, read: func(s projectAdviceSnapshot) string { return s.NextAction }},
	}
	for _, update := range updates {
		current, err := c.projectAdvice(ctx, issue.IssueID, issue.ProjectID, fields)
		if err != nil {
			return err
		}
		if current.ItemID != issue.ProjectItemID || current.OptionID != issue.StatusOptionID || !current.UpdatedAt.Equal(issue.StatusUpdatedAt) {
			return errors.New("project status changed during advice reconciliation")
		}
		if update.read(current) == update.want {
			continue
		}
		var mutation string
		variables := map[string]any{"project": issue.ProjectID, "item": issue.ProjectItemID, "field": update.id}
		if update.want == "" {
			mutation = `mutation($project:ID!,$item:ID!,$field:ID!){clearProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field}){projectV2Item{id}}}`
		} else {
			mutation = `mutation($project:ID!,$item:ID!,$field:ID!,$text:String!){updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{text:$text}}){projectV2Item{id}}}`
			variables["text"] = update.want
		}
		var data struct {
			UpdateProjectV2ItemFieldValue *struct{ ProjectV2Item *struct{ ID string } }
			ClearProjectV2ItemFieldValue  *struct{ ProjectV2Item *struct{ ID string } }
		}
		if err := c.GraphQL(ctx, mutation, variables, &data); err != nil {
			return err
		}
		var updatedID string
		if data.UpdateProjectV2ItemFieldValue != nil && data.UpdateProjectV2ItemFieldValue.ProjectV2Item != nil {
			updatedID = data.UpdateProjectV2ItemFieldValue.ProjectV2Item.ID
		}
		if data.ClearProjectV2ItemFieldValue != nil && data.ClearProjectV2ItemFieldValue.ProjectV2Item != nil {
			updatedID = data.ClearProjectV2ItemFieldValue.ProjectV2Item.ID
		}
		if updatedID != issue.ProjectItemID {
			return errors.New("project advice update identity unavailable")
		}
	}
	return nil
}

func validAdviceIdentity(value string) bool {
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n\x00")
}

func validAdviceName(value string) bool {
	return value != "" && len(value) <= 100 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func validAdviceText(value string) bool {
	return len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

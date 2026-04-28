package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// API is the subset of *Client the tools need; lets tests inject a fake.
type API interface {
	ListTasks(ctx context.Context, f ListFilter) ([]Task, error)
	CreateTask(ctx context.Context, req CreateRequest) (*Task, error)
	CompleteTask(ctx context.Context, id string) error
	UpdateTask(ctx context.Context, id string, req UpdateRequest) (*Task, error)
}

// ListTool

type ListTool struct{ Client API }

func (ListTool) Name() string        { return "todoist.list_tasks" }
func (ListTool) Description() string {
	return `List Todoist tasks. The "filter" arg uses Todoist filter syntax — NOT plain English. ` +
		`Common expressions: "today", "tomorrow", "overdue", "next 7 days", "no date", ` +
		`"p1"/"p2"/"p3"/"p4" (priority), "#ProjectName" (tasks in a project — for the inbox use "#Inbox"), ` +
		`"@labelname" (label), "assigned to: me". Combine with " & " (and) or " | " (or). ` +
		`Omit "filter" entirely to list ALL tasks across every project.`
}
func (ListTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"filter":{"type":"string","description":"Todoist filter expression. Examples: '#Inbox', 'today', 'overdue', 'p1', '#Work & today', '@home'. Note: 'inbox' alone is NOT valid; use '#Inbox'."},
			"project_id":{"type":"string","description":"only for known project ids; prefer 'filter' with #ProjectName"},
			"label":{"type":"string"}
		},
		"required":[]
	}`)
}
func (lt ListTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Filter    string `json:"filter"`
		ProjectID string `json:"project_id"`
		Label     string `json:"label"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
	}
	tasks, err := lt.Client.ListTasks(ctx, ListFilter{Filter: p.Filter, ProjectID: p.ProjectID, Label: p.Label})
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "no tasks", nil
	}
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "[%s] p%d %s", t.ID, t.Priority, t.Content)
		if t.Due != nil && t.Due.Date != "" {
			fmt.Fprintf(&b, " (due %s)", t.Due.Date)
		}
		if len(t.Labels) > 0 {
			fmt.Fprintf(&b, " #%s", strings.Join(t.Labels, " #"))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// CreateTool

type CreateTool struct{ Client API }

func (CreateTool) Name() string        { return "todoist.create_task" }
func (CreateTool) Description() string { return "Create a new Todoist task. Returns the new task ID." }
func (CreateTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"content":{"type":"string","description":"task title"},
			"description":{"type":"string"},
			"project_id":{"type":"string"},
			"priority":{"type":"integer","minimum":1,"maximum":4,"description":"1=normal, 4=urgent"},
			"due_string":{"type":"string","description":"natural-language due, e.g. tomorrow at 5pm"},
			"due_date":{"type":"string","description":"YYYY-MM-DD"},
			"labels":{"type":"array","items":{"type":"string"}}
		},
		"required":["content"]
	}`)
}
func (ct CreateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var req CreateRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if req.Content == "" {
		return "", fmt.Errorf("content required")
	}
	t, err := ct.Client.CreateTask(ctx, req)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("created task %s: %q", t.ID, t.Content), nil
}

// CompleteTool

type CompleteTool struct{ Client API }

func (CompleteTool) Name() string        { return "todoist.complete_task" }
func (CompleteTool) Description() string { return "Mark a Todoist task complete." }
func (CompleteTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{"id":{"type":"string","description":"task id"}},
		"required":["id"]
	}`)
}
func (ct CompleteTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct{ ID string `json:"id"` }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.ID == "" {
		return "", fmt.Errorf("id required")
	}
	if err := ct.Client.CompleteTask(ctx, p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("completed task %s", p.ID), nil
}

// UpdateTool

type UpdateTool struct{ Client API }

func (UpdateTool) Name() string        { return "todoist.update_task" }
func (UpdateTool) Description() string { return "Update a Todoist task. Only fields you want to change need to be provided." }
func (UpdateTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"properties":{
			"id":{"type":"string"},
			"content":{"type":"string"},
			"description":{"type":"string"},
			"priority":{"type":"integer","minimum":1,"maximum":4},
			"due_string":{"type":"string"},
			"labels":{"type":"array","items":{"type":"string"}}
		},
		"required":["id"]
	}`)
}
func (ut UpdateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		ID          string   `json:"id"`
		Content     *string  `json:"content,omitempty"`
		Description *string  `json:"description,omitempty"`
		Priority    *int     `json:"priority,omitempty"`
		DueString   *string  `json:"due_string,omitempty"`
		Labels      []string `json:"labels,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if p.ID == "" {
		return "", fmt.Errorf("id required")
	}
	t, err := ut.Client.UpdateTask(ctx, p.ID, UpdateRequest{
		Content: p.Content, Description: p.Description, Priority: p.Priority, DueString: p.DueString, Labels: p.Labels,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("updated task %s", t.ID), nil
}

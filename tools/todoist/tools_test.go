package todoist

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	listOut    []Task
	listErr    error
	createOut  *Task
	completeID string
	updateOut  *Task
}

func (f *fakeAPI) ListTasks(_ context.Context, _ ListFilter) ([]Task, error) {
	return f.listOut, f.listErr
}
func (f *fakeAPI) CreateTask(_ context.Context, _ CreateRequest) (*Task, error) {
	return f.createOut, nil
}
func (f *fakeAPI) CompleteTask(_ context.Context, id string) error {
	f.completeID = id
	return nil
}
func (f *fakeAPI) UpdateTask(_ context.Context, _ string, _ UpdateRequest) (*Task, error) {
	return f.updateOut, nil
}

func TestListTool(t *testing.T) {
	api := &fakeAPI{listOut: []Task{
		{ID: "1", Content: "Buy milk", Priority: 3, Due: &Due{Date: "2026-04-29"}, Labels: []string{"home"}},
	}}
	out, err := ListTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"filter":"today"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Buy milk")
	require.Contains(t, out, "2026-04-29")
	require.Contains(t, out, "#home")
}

func TestListTool_Empty(t *testing.T) {
	api := &fakeAPI{listOut: nil}
	out, err := ListTool{Client: api}.Execute(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "no tasks", out)
}

func TestListTool_Error(t *testing.T) {
	api := &fakeAPI{listErr: errors.New("boom")}
	_, err := ListTool{Client: api}.Execute(context.Background(), nil)
	require.Error(t, err)
}

func TestCreateTool_RequiresContent(t *testing.T) {
	_, err := CreateTool{Client: &fakeAPI{}}.Execute(context.Background(), json.RawMessage(`{}`))
	require.Error(t, err)
}

func TestCreateTool_Happy(t *testing.T) {
	api := &fakeAPI{createOut: &Task{ID: "100", Content: "X"}}
	out, err := CreateTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"content":"X"}`))
	require.NoError(t, err)
	require.Contains(t, out, "100")
}

func TestCompleteTool(t *testing.T) {
	api := &fakeAPI{}
	out, err := CompleteTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"id":"42"}`))
	require.NoError(t, err)
	require.Contains(t, out, "42")
	require.Equal(t, "42", api.completeID)
}

func TestUpdateTool(t *testing.T) {
	api := &fakeAPI{updateOut: &Task{ID: "42"}}
	out, err := UpdateTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"id":"42","priority":4}`))
	require.NoError(t, err)
	require.Contains(t, out, "42")
}

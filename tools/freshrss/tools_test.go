package freshrss

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeAPI struct {
	listOut []Article
	listErr error
	getOut  *Article
	getErr  error
	markID  string
	markAct Action
	markErr error
	gotOpts ListOpts
}

func (f *fakeAPI) List(_ context.Context, o ListOpts) ([]Article, error) {
	f.gotOpts = o
	return f.listOut, f.listErr
}
func (f *fakeAPI) Get(_ context.Context, _ string) (*Article, error) {
	return f.getOut, f.getErr
}
func (f *fakeAPI) Mark(_ context.Context, id string, act Action) error {
	f.markID, f.markAct = id, act
	return f.markErr
}

func TestListTool_DefaultsToUnread(t *testing.T) {
	api := &fakeAPI{listOut: []Article{
		{ID: "x", Title: "Hello", URL: "u", Feed: "F", Published: time.Now(), Starred: true},
	}}
	out, err := ListTool{Client: api}.Execute(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, FilterUnread, api.gotOpts.Filter)
	require.Contains(t, out, "Hello")
	require.Contains(t, out, "★")
}

func TestListTool_Starred(t *testing.T) {
	api := &fakeAPI{}
	_, err := ListTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"filter":"starred"}`))
	require.NoError(t, err)
	require.Equal(t, FilterStarred, api.gotOpts.Filter)
}

func TestListTool_Empty(t *testing.T) {
	api := &fakeAPI{}
	out, err := ListTool{Client: api}.Execute(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "no matching articles", out)
}

func TestListTool_Error(t *testing.T) {
	api := &fakeAPI{listErr: errors.New("boom")}
	_, err := ListTool{Client: api}.Execute(context.Background(), nil)
	require.Error(t, err)
}

func TestGetTool(t *testing.T) {
	api := &fakeAPI{getOut: &Article{ID: "x", Title: "T", URL: "U", Feed: "F", Summary: "<p>body</p>", Published: time.Now()}}
	out, err := GetTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"id":"x"}`))
	require.NoError(t, err)
	require.Contains(t, out, "Title: T")
	require.Contains(t, out, "<p>body</p>")
}

func TestMarkTool(t *testing.T) {
	api := &fakeAPI{}
	out, err := MarkTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"id":"x","action":"read"}`))
	require.NoError(t, err)
	require.Contains(t, out, "read: x")
	require.Equal(t, ActionMarkRead, api.markAct)
	require.Equal(t, "x", api.markID)

	_, err = MarkTool{Client: api}.Execute(context.Background(), json.RawMessage(`{"id":"x","action":"star"}`))
	require.NoError(t, err)
	require.Equal(t, ActionStar, api.markAct)
}

func TestMarkTool_BadAction(t *testing.T) {
	_, err := MarkTool{Client: &fakeAPI{}}.Execute(context.Background(), json.RawMessage(`{"id":"x","action":"nope"}`))
	require.Error(t, err)
}

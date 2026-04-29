package obs_test

import (
	"context"
	"errors"
	"testing"

	"darek/obs"
)

func TestDep_CallsFnExactlyOnce(t *testing.T) {
	calls := 0
	err := obs.Dep(context.Background(), "openai_chat", "chat", func(ctx context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
}

func TestDep_PropagatesError(t *testing.T) {
	want := errors.New("boom")
	got := obs.Dep(context.Background(), "openai_chat", "chat", func(ctx context.Context) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDep_RejectsEmptyDepOrOp(t *testing.T) {
	if err := obs.Dep(context.Background(), "", "chat", func(ctx context.Context) error { return nil }); err == nil {
		t.Error("expected error on empty dep")
	}
	if err := obs.Dep(context.Background(), "openai_chat", "", func(ctx context.Context) error { return nil }); err == nil {
		t.Error("expected error on empty op")
	}
}

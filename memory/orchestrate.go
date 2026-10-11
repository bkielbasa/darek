package memory

import (
	"context"
	"fmt"

	"darek/tools/whatsapp"
)

// memoriesLookbackDays bounds how far back extraction reaches. Bounded so a
// long darek-serve outage doesn't dump weeks of messages into one run.
const memoriesLookbackDays = 7

// TranscriptSource is the subset of *whatsapp.Store used by BuildMemories.
type TranscriptSource interface {
	OptedInGroups(ctx context.Context) ([]whatsapp.Group, error)
	UnextractedMessages(ctx context.Context, groupJID string, lookbackDays int) ([]whatsapp.Message, error)
	MarkMemoriesExtracted(ctx context.Context, ids []string) error
}

// ExtractorInterface is the subset of *Extractor used by BuildMemories.
type ExtractorInterface interface {
	Extract(ctx context.Context, groupName string, msgs []whatsapp.Message) ([]Candidate, error)
}

// Stats summarizes one BuildMemories run.
type Stats struct {
	Groups       int
	Extracted    int
	Saved        int
	FailedGroups int
}

// BuildMemories extracts and persists memories for every opted-in group with
// unextracted messages. Per-group failures are logged and skipped; only an
// OptedInGroups failure aborts the run.
func BuildMemories(
	ctx context.Context,
	src TranscriptSource,
	store *Store,
	extractor ExtractorInterface,
	logger func(format string, a ...any),
) (Stats, error) {
	if logger == nil {
		logger = func(string, ...any) {}
	}
	var st Stats

	groups, err := src.OptedInGroups(ctx)
	if err != nil {
		return st, fmt.Errorf("opted-in groups: %w", err)
	}
	st.Groups = len(groups)

	for _, g := range groups {
		msgs, err := src.UnextractedMessages(ctx, g.JID, memoriesLookbackDays)
		if err != nil {
			logger("memory: unextracted for %q: %v\n", g.Name, err)
			st.FailedGroups++
			continue
		}
		if len(msgs) == 0 {
			continue
		}

		cands, err := extractor.Extract(ctx, g.Name, msgs)
		if err != nil {
			logger("memory: extract for %q: %v\n", g.Name, err)
			st.FailedGroups++
			continue
		}
		if store.m != nil {
			store.m.MemoriesExtracted.Add(ctx, int64(len(cands)))
		}

		ids := messageIDs(msgs)
		if len(cands) > 0 {
			n, err := store.SaveMemories(ctx, cands, g.Name, ids)
			if err != nil {
				logger("memory: save for %q: %v\n", g.Name, err)
				st.FailedGroups++
				continue
			}
			st.Extracted += len(cands)
			st.Saved += n
		}

		if err := src.MarkMemoriesExtracted(ctx, ids); err != nil {
			logger("memory: mark extracted for %q: %v\n", g.Name, err)
		}
	}
	return st, nil
}

func messageIDs(msgs []whatsapp.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

package recommend

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Embedder computes embedding vectors for texts. *llm.Client satisfies it.
type Embedder interface {
	Embed(ctx context.Context, model string, inputs []string) ([][]float32, error)
}

// embedStore is the subset of Store used by EnsureEmbeddings, extracted so the
// batching logic can be unit-tested without a database.
type embedStore interface {
	PendingEmbeddings(ctx context.Context, limit int) ([]EmbedText, error)
	SetEmbedding(ctx context.Context, id uuid.UUID, vec []float32) error
}

// EnsureEmbeddings embeds pending links in batches. limit<=0 means "all
// currently pending". It returns how many links were embedded.
func EnsureEmbeddings(ctx context.Context, store embedStore, emb Embedder, model string, limit, batch int) (int, error) {
	if batch <= 0 {
		batch = 64
	}
	done := 0
	for {
		n := batch
		if limit > 0 && limit-done < n {
			n = limit - done
		}
		if n <= 0 {
			break
		}

		pending, err := store.PendingEmbeddings(ctx, n)
		if err != nil {
			return done, err
		}
		if len(pending) == 0 {
			break
		}

		texts := make([]string, len(pending))
		for i, p := range pending {
			texts[i] = p.Text
		}
		vecs, err := emb.Embed(ctx, model, texts)
		if err != nil {
			return done, fmt.Errorf("embed batch: %w", err)
		}
		if len(vecs) != len(pending) {
			return done, fmt.Errorf("embed: got %d vectors for %d inputs", len(vecs), len(pending))
		}
		for i, v := range vecs {
			if err := store.SetEmbedding(ctx, pending[i].ID, v); err != nil {
				return done, err
			}
			done++
		}
		if len(pending) < n {
			break
		}
	}
	return done, nil
}

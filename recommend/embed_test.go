package recommend

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeEmbedStore struct {
	mu      sync.Mutex
	pending []EmbedText
	set     map[uuid.UUID][]float32
}

func newFakeEmbedStore(n int) *fakeEmbedStore {
	fs := &fakeEmbedStore{set: map[uuid.UUID][]float32{}}
	for i := 0; i < n; i++ {
		fs.pending = append(fs.pending, EmbedText{ID: uuid.New(), Text: "text"})
	}
	return fs
}

func (f *fakeEmbedStore) PendingEmbeddings(_ context.Context, limit int) ([]EmbedText, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit > len(f.pending) {
		limit = len(f.pending)
	}
	return append([]EmbedText(nil), f.pending[:limit]...), nil
}

func (f *fakeEmbedStore) SetEmbedding(_ context.Context, id uuid.UUID, vec []float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set[id] = vec
	for i, p := range f.pending {
		if p.ID == id {
			f.pending = append(f.pending[:i], f.pending[i+1:]...)
			break
		}
	}
	return nil
}

type fakeEmbedder struct {
	mu      sync.Mutex
	batches []int
}

func (f *fakeEmbedder) Embed(_ context.Context, _ string, inputs []string) ([][]float32, error) {
	f.mu.Lock()
	f.batches = append(f.batches, len(inputs))
	f.mu.Unlock()
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = vec(i)
	}
	return out, nil
}

func TestEnsureEmbeddings_BatchesUntilDrained(t *testing.T) {
	store := newFakeEmbedStore(5)
	emb := &fakeEmbedder{}

	done, err := EnsureEmbeddings(context.Background(), store, emb, "m", 0, 2)
	require.NoError(t, err)
	require.Equal(t, 5, done)
	require.Equal(t, []int{2, 2, 1}, emb.batches)
	require.Len(t, store.set, 5)
}

func TestEnsureEmbeddings_RespectsLimit(t *testing.T) {
	store := newFakeEmbedStore(10)
	emb := &fakeEmbedder{}

	done, err := EnsureEmbeddings(context.Background(), store, emb, "m", 3, 2)
	require.NoError(t, err)
	require.Equal(t, 3, done)
	require.Equal(t, []int{2, 1}, emb.batches)
}

func TestEnsureEmbeddings_Empty(t *testing.T) {
	store := newFakeEmbedStore(0)
	done, err := EnsureEmbeddings(context.Background(), store, &fakeEmbedder{}, "m", 0, 2)
	require.NoError(t, err)
	require.Zero(t, done)
}

func vec(i int) []float32 {
	v := make([]float32, 4)
	v[i%4] = 1
	return v
}

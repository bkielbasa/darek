package recommend

// Options controls a recommendation run.
type Options struct {
	Count          int
	CandidatePool  int
	LookbackDays   int
	CooldownDays   int
	MinLiked       int
	EmbeddingModel string
	EmbedLimit     int
	EmbedBatch     int
}

// Result summarizes a recommendation run.
type Result struct {
	Suggestions []Suggestion
	Embedded    int
	Candidates  int
	Vector      bool
}

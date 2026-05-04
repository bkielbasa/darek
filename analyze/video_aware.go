package analyze

import (
	"context"
	"fmt"

	"darek/tools/youtube"
)

// Transcriber is the subset of *youtube.Client used by VideoAwareAnalyzer.
// Defined as an interface so tests can supply a fake; *youtube.Client
// satisfies it without changes.
type Transcriber interface {
	Fetch(ctx context.Context, rawURL, lang string) (youtube.Result, error)
}

// VideoAwareAnalyzer wraps *Analyzer. For YouTube URLs (anything
// youtube.ExtractVideoID accepts) it fetches the transcript and uses it as
// Input.Body, ignoring whatever Body the caller passed. For non-YouTube
// URLs it delegates straight to the inner Analyzer.
type VideoAwareAnalyzer struct {
	inner       *Analyzer
	transcriber Transcriber
}

// NewVideoAware constructs a VideoAwareAnalyzer.
func NewVideoAware(inner *Analyzer, t Transcriber) *VideoAwareAnalyzer {
	return &VideoAwareAnalyzer{inner: inner, transcriber: t}
}

// Analyze fetches the transcript for YouTube URLs (replacing Input.Body) and
// delegates to the inner Analyzer.
func (v *VideoAwareAnalyzer) Analyze(ctx context.Context, in Input) (Output, error) {
	if _, err := youtube.ExtractVideoID(in.URL); err == nil {
		res, terr := v.transcriber.Fetch(ctx, in.URL, "")
		if terr != nil {
			return Output{}, fmt.Errorf("youtube transcript: %w", terr)
		}
		in.Body = res.Text
	}
	return v.inner.Analyze(ctx, in)
}

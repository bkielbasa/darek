package blogmarketing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"darek/tools/blogfeed"

	"github.com/openai/openai-go"
)

// Platform identifies a social network. Values double as Todoist label names.
type Platform string

const (
	PlatformX        Platform = "x"
	PlatformMastodon Platform = "mastodon"
	PlatformLinkedIn Platform = "linkedin"
)

// AllPlatforms is the canonical iteration order for the 9-task fan-out.
var AllPlatforms = []Platform{PlatformX, PlatformMastodon, PlatformLinkedIn}

// Cadence identifies which beat in the campaign a task belongs to. Values
// double as Todoist label names.
type Cadence string

const (
	CadenceLaunch       Cadence = "launch"
	CadenceReshare2W    Cadence = "reshare-2w"
	CadenceResurface3Mo Cadence = "resurface-3mo"
)

// AllCadences is the canonical iteration order.
var AllCadences = []Cadence{CadenceLaunch, CadenceReshare2W, CadenceResurface3Mo}

// Drafts is the 3x3 grid of LLM-generated post text.
type Drafts map[Platform]map[Cadence]string

// Drafter produces post drafts for a feed entry.
type Drafter interface {
	Draft(ctx context.Context, e blogfeed.Entry) (Drafts, error)
}

// Chat is the subset of *llm.Client that OpenAIDrafter uses. Defined as an
// interface so tests can supply a fake.
type Chat interface {
	Chat(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error)
}

// OpenAIDrafter is the production Drafter implementation.
type OpenAIDrafter struct{ llm Chat }

func NewOpenAIDrafter(c Chat) *OpenAIDrafter { return &OpenAIDrafter{llm: c} }

const drafterSystemPrompt = `You are drafting social-media posts to promote a blog post.
For each platform (x, mastodon, linkedin) and each cadence (launch, reshare, resurface), produce ONE ready-to-send post.

Voice constraints:
- launch: announce the post is live now. Include the URL.
- reshare: posted ~2 weeks later. Frame as a re-share / "in case you missed it". Include the URL.
- resurface: posted ~3 months later. Frame as evergreen / still useful. Include the URL.

Platform constraints:
- x: keep under 280 characters total (the URL counts as 23). Direct, hook-led.
- mastodon: under 500 characters. Use 2-4 hashtags placed inline or at the end.
- linkedin: 2-4 short paragraphs, professional tone, hashtags at the very end.

Reply with strict JSON only, in this exact shape:
{
  "x":        {"launch":"...","reshare":"...","resurface":"..."},
  "mastodon": {"launch":"...","reshare":"...","resurface":"..."},
  "linkedin": {"launch":"...","reshare":"...","resurface":"..."}
}
No prose outside the JSON. No markdown fences.`

// Draft sends the entry to the model and parses out the 9 drafts.
func (d *OpenAIDrafter) Draft(ctx context.Context, e blogfeed.Entry) (Drafts, error) {
	user := fmt.Sprintf("Title: %s\nURL: %s\nSummary:\n%s", e.Title, e.URL, e.Summary)
	resp, err := d.llm.Chat(ctx, openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(drafterSystemPrompt),
			openai.UserMessage(user),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("blogmarketing draft: chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("blogmarketing draft: empty choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Message.Content)

	type cellNames struct {
		Launch    string `json:"launch"`
		Reshare   string `json:"reshare"`
		Resurface string `json:"resurface"`
	}
	var raw struct {
		X        cellNames `json:"x"`
		Mastodon cellNames `json:"mastodon"`
		LinkedIn cellNames `json:"linkedin"`
	}
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return nil, fmt.Errorf("blogmarketing draft: bad json from model: %w", err)
	}

	drafts := Drafts{
		PlatformX:        {CadenceLaunch: raw.X.Launch, CadenceReshare2W: raw.X.Reshare, CadenceResurface3Mo: raw.X.Resurface},
		PlatformMastodon: {CadenceLaunch: raw.Mastodon.Launch, CadenceReshare2W: raw.Mastodon.Reshare, CadenceResurface3Mo: raw.Mastodon.Resurface},
		PlatformLinkedIn: {CadenceLaunch: raw.LinkedIn.Launch, CadenceReshare2W: raw.LinkedIn.Reshare, CadenceResurface3Mo: raw.LinkedIn.Resurface},
	}
	for _, p := range AllPlatforms {
		for _, c := range AllCadences {
			if strings.TrimSpace(drafts[p][c]) == "" {
				return nil, fmt.Errorf("blogmarketing draft: missing %s/%s", p, c)
			}
		}
	}
	return drafts, nil
}

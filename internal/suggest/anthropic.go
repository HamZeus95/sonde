package suggest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Model is what drafts the assertions. Opus 5: the drafting is the only part of
// Sonde where judgement is wanted, and a weaker model spends the reviewer's
// attention on rejecting its output.
const Model = "claude-opus-5"

// maxTokens is generous because a long runbook can justify a dozen assertions,
// and a draft truncated mid-block is a draft that fails validation for a reason
// that has nothing to do with the runbook.
const maxTokens = 16000

// toolName is the single tool the model is given. Forcing it means the response
// is a structured proposal rather than prose about a proposal.
const toolName = "propose_assertions"

// Anthropic drafts assertions with the Claude API.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic builds a drafter. The key comes from the environment, never from
// a flag: a key on a command line lands in shell history and in CI logs.
func NewAnthropic() (*Anthropic, error) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set: sonde suggest is the one command that needs it")
	}
	model := os.Getenv("SONDE_SUGGEST_MODEL")
	if model == "" {
		model = Model
	}
	return &Anthropic{client: anthropic.NewClient(option.WithHeader("x-app", "sonde-suggest")), model: model}, nil
}

// Draft implements Drafter.
func (a *Anthropic) Draft(ctx context.Context, req Request) ([]Draft, error) {
	payload, err := json.MarshalIndent(requestPayload(req), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	tool := anthropic.ToolParam{
		Name:        toolName,
		Description: anthropic.String("Propose assertion blocks for this runbook."),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: schemaProperties()},
	}

	response, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: maxTokens,
		// Adaptive thinking: deciding which claims in a document are testable,
		// and which of them the catalogue can actually express, is exactly the
		// kind of judgement worth spending reasoning on.
		Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
		System: []anthropic.TextBlockParam{{
			Text: systemPrompt,
		}},
		Tools: []anthropic.ToolUnionParam{{OfTool: &tool}},
		// One tool and no alternative: the answer is a proposal or nothing.
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: toolName},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(string(payload))),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("draft assertions: %w", err)
	}

	// A refusal is an answer, not a crash: say so plainly rather than
	// reporting an empty proposal.
	if response.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("the model declined to draft assertions for this document (%s)", response.StopDetails.Category)
	}

	for _, block := range response.Content {
		use, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok || use.Name != toolName {
			continue
		}
		var proposal struct {
			Assertions []Draft `json:"assertions"`
		}
		// Parsed, never string-matched: current models vary their JSON
		// escaping, and raw matching on the serialised input breaks on it.
		if err := json.Unmarshal([]byte(use.JSON.Input.Raw()), &proposal); err != nil {
			return nil, fmt.Errorf("read the model's proposal: %w", err)
		}
		return proposal.Assertions, nil
	}
	return nil, fmt.Errorf("the model returned no proposal")
}

// requestPayload is what the model reads: the prose, the headings it may anchor
// to, what the runbook already asserts, and the catalogue it must draft within.
func requestPayload(req Request) map[string]any {
	return map[string]any{
		"path":             req.Path,
		"runbook_id":       req.Meta.ID,
		"environment":      req.Meta.Environment,
		"existing_checks":  req.Existing,
		"headings":         req.Headings,
		"check_catalogue":  req.Catalogue,
		"runbook_markdown": req.Prose,
	}
}

func schemaProperties() map[string]any {
	return map[string]any{
		"assertions": map[string]any{
			"type":        "array",
			"description": "The assertions worth adding. Empty is a valid answer.",
			"items": map[string]any{
				"type":     "object",
				"required": []string{"id", "kind", "check", "heading_index", "fields", "rationale"},
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "Unique within the runbook. Letters, digits, dot, dash, underscore.",
					},
					"kind":  map[string]any{"type": "string"},
					"check": map[string]any{"type": "string"},
					"heading_index": map[string]any{
						"type":        "integer",
						"description": "The index of the heading whose step this assertion belongs to.",
					},
					"fields": map[string]any{
						"type":        "string",
						"description": "The YAML body of the block, without id, kind or check. One field per line.",
					},
					"rationale": map[string]any{
						"type":        "string",
						"description": "One sentence: which sentence in the runbook this makes testable.",
					},
				},
			},
		},
	}
}

// systemPrompt is deliberately about restraint. The failure mode that matters
// is not a missing suggestion — it is a plausible assertion about a resource
// the document never mentions, which a reviewer accepts and which then fails
// forever against infrastructure that was never wrong.
const systemPrompt = `You draft assertions for Sonde, a test harness for operational runbooks.

The input is a runbook's prose. Your job is to find the claims it makes about live
infrastructure and express each one as an assertion from the catalogue you are given.

Rules, in order of importance:

1. Only assert what the document states. Every field you write must be traceable to
   something in the prose — a resource name, a URL, a group, a command. If the runbook
   does not say which namespace a deployment is in, do not guess one: either omit the
   field or skip the assertion. A confident wrong assertion is worse than no assertion,
   because it fails forever and teaches the team to ignore Sonde.
2. Use only the kind and check pairs in the catalogue, and only the fields each one
   lists. Nothing else parses.
3. Do not restate assertions the runbook already makes. They are listed for you.
4. Prefer the assertion that would catch real rot. A runbook that says "scale
   deployment/payments-api" is worth a resource_exists and a can_i on the verb it
   tells the reader to run; it is not worth an assertion about every object mentioned
   in passing.
5. Anchor each assertion to the heading whose step it belongs to.
6. Returning an empty list is correct when the prose makes no testable claim. Say
   nothing rather than inventing something.

The rationale is read by a human deciding whether to commit your suggestion. Make it
one sentence naming the claim in the runbook that the assertion tests.`

package llm

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type anthropicProvider struct {
	client anthropic.Client
}

func newAnthropicProvider(cfg *providerConfig) (*anthropicProvider, error) {
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	// If no API key is set, the SDK reads ANTHROPIC_API_KEY from env.

	client := anthropic.NewClient(opts...)
	return &anthropicProvider{client: client}, nil
}

func (p *anthropicProvider) Name() string { return "anthropic" }

func (p *anthropicProvider) Complete(ctx context.Context, req *Request) (*Response, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 1024
	}

	var msgs []anthropic.MessageParam
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		}
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: maxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{
			{Text: req.System},
		}
	}

	result, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return nil, err
	}

	var content string
	for _, block := range result.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.TextBlock:
			content += variant.Text
		}
	}

	return &Response{
		Content:    content,
		InputToks:  int(result.Usage.InputTokens),
		OutputToks: int(result.Usage.OutputTokens),
	}, nil
}

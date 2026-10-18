package openrouter

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch10/agent"
)

// ErrNoLogprobs means the reply came back without token probabilities:
// the model or provider doesn't report them.
var ErrNoLogprobs = errors.New("openrouter: reply has no logprobs")

// ErrLogprobsMismatch means the tokens a provider reported don't spell
// out the text it returned, so their probabilities can't be trusted.
var ErrLogprobsMismatch = errors.New(
	"openrouter: logprobs don't match the reply's text")

// tag::token[]

// A Token is one token the model wrote, with its probability and, at
// the same position, the most likely tokens it chose from.
type Token struct {
	Text string
	P    float64
	Top  []Token // most likely first; usually includes this token
}

// A TokenReply is a reply as tokens, with why the model stopped.
type TokenReply struct {
	Tokens       []Token
	FinishReason string
}

// end::token[]

// tag::probe[]

// Probe asks the model to continue the conversation at the given
// temperature and returns each token it wrote, with the top most
// likely alternatives at each position.
func (c *Client) Probe(ctx context.Context, msgs []agent.Message,
	temperature float64, top, maxTokens int) (TokenReply, error) {
	wire := c.wireRequest(agent.Request{Messages: msgs,
		MaxTokens: maxTokens})
	wire.Temperature = &temperature
	wire.Logprobs, wire.TopLogprobs = true, top // <1>
	if wire.Provider == nil {
		wire.Provider = &routing{}
	}
	wire.Provider.RequireParameters = true // <2>
	r, err := c.send(ctx, wire)
	if err != nil {
		return TokenReply{}, fmt.Errorf("probe: %w", err)
	}
	ch := r.Choices[0]
	if ch.Logprobs == nil || len(ch.Logprobs.Content) == 0 {
		return TokenReply{}, fmt.Errorf("probe: %w", ErrNoLogprobs)
	}
	if !spells(ch.Logprobs.Content, ch.Message.Content.Text) { // <3>
		return TokenReply{}, fmt.Errorf("probe: %w (provider %s)",
			ErrLogprobsMismatch, r.Provider)
	}
	return TokenReply{tokens(ch.Logprobs.Content), ch.FinishReason}, nil
}

// end::probe[]

// spells reports whether the tokens spell out text. They may run on
// past it, with an end-of-message token that the text leaves out. A
// character can be split across tokens, each of which then shows as
// "\uFFFD", so the tokens' bytes are used when the provider sends them.
func spells(lps []tokenLogprob, text string) bool {
	var b []byte
	for _, lp := range lps {
		if lp.Bytes != nil {
			for _, x := range lp.Bytes {
				b = append(b, byte(x))
			}
		} else {
			b = append(b, lp.Token...)
		}
	}
	return strings.HasPrefix(string(b), text)
}

// tokens converts log probabilities to probabilities.
func tokens(lps []tokenLogprob) []Token {
	var out []Token
	for _, lp := range lps {
		out = append(out, Token{Text: lp.Token,
			P: math.Exp(lp.Logprob), Top: tokens(lp.Top)})
	}
	return out
}

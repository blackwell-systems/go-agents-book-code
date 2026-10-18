package orsdk

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/optionalnullable"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// tag::chat-message[]

// chatMessage maps one message onto the SDK's union of message types.
// A message marked as a cache point goes as a list of one text part
// carrying the marker, as chapter 8's client sends it.
func chatMessage(m agent.Message) components.ChatMessages {
	switch m.Role {
	case "system":
		return components.CreateChatMessagesSystem(
			components.ChatSystemMessage{Content: systemContent(m)})
	case "assistant":
		return components.CreateChatMessagesAssistant(assistant(m))
	case "tool":
		return components.CreateChatMessagesTool(
			components.ChatToolMessage{ToolCallID: m.ToolCallID,
				Content: toolContent(m)})
	}
	return components.CreateChatMessagesUser(components.ChatUserMessage{
		Content: userContent(m)})
}

// end::chat-message[]

func systemContent(m agent.Message) components.ChatSystemMessageContent {
	if !m.CachePoint {
		return components.CreateChatSystemMessageContentStr(m.Content)
	}
	return components.CreateChatSystemMessageContentArrayOfChatContentText(
		[]components.ChatContentText{cachedText(m.Content)})
}

func userContent(m agent.Message) components.ChatUserMessageContent {
	if !m.CachePoint {
		return components.CreateChatUserMessageContentStr(m.Content)
	}
	return components.CreateChatUserMessageContentArrayOfChatContentItems(
		[]components.ChatContentItems{
			components.CreateChatContentItemsText(cachedText(m.Content))})
}

// toolContent is a tool's result, with the cache marker when the
// message is a cache point: chapter 8's CacheMarks marks the last
// message, which is most often a tool's result.
func toolContent(m agent.Message) components.ChatToolMessageContent {
	text := m.Content
	if m.IsError {
		text = "Error: " + text // as chapter 4's client does
	}
	if !m.CachePoint {
		return components.CreateChatToolMessageContentStr(text)
	}
	return components.CreateChatToolMessageContentArrayOfChatContentItems(
		[]components.ChatContentItems{
			components.CreateChatContentItemsText(cachedText(text))})
}

func cachedText(text string) components.ChatContentText {
	return components.ChatContentText{
		Type: components.ChatContentTextTypeText,
		Text: text, CacheControl: &components.ChatContentCacheControl{
			Type: components.ChatContentCacheControlTypeEphemeral}}
}

// tag::assistant[]

// assistant maps an assistant message: its text, its tool calls, and
// its reasoning, which goes back only if OpenRouter produced it.
func assistant(m agent.Message) components.ChatAssistantMessage {
	var out components.ChatAssistantMessage
	if m.Content != "" {
		c := assistantContent(m)
		out.Content = optionalnullable.From(&c)
	}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, components.ChatToolCall{
			ID: tc.ID, Type: components.ChatToolCallTypeFunction,
			Function: components.ChatToolCallFunction{Name: tc.Name,
				Arguments: string(tc.Arguments)}})
	}
	if m.ReasoningFrom == "" && m.ReasoningDetails != nil {
		// A record the SDK's union can't hold goes without its
		// reasoning rather than failing the call.
		if json.Unmarshal(m.ReasoningDetails, &out.ReasoningDetails) != nil {
			out.ReasoningDetails = nil
		}
	}
	return out
}

// end::assistant[]

// chatTool maps a tool definition. The SDK holds a schema as a Go map,
// so the schema it sends has its properties in key order rather than
// the order chapter 7 generated: the same bytes on every request, so
// caching still works, but different bytes from the hand-written
// client's.
func chatTool(t agent.ToolDef) (components.ChatFunctionTool, error) {
	f := components.ChatFunctionToolFunctionFunction{Name: t.Name,
		Description: &t.Description}
	if err := json.Unmarshal(t.Parameters, &f.Parameters); err != nil {
		return components.ChatFunctionTool{},
			fmt.Errorf("tool %s: schema: %w", t.Name, err)
	}
	if t.Strict {
		strict := true
		f.Strict = optionalnullable.From(&strict)
	}
	return components.CreateChatFunctionToolChatFunctionToolFunction(
		components.ChatFunctionToolFunction{
			Type:     components.ChatFunctionToolTypeFunction,
			Function: f}), nil
}

func responseFormat(schema jsontext.Value) (*components.ResponseFormat,
	error) {
	cfg := components.ChatJSONSchemaConfig{Name: "reply"}
	if err := json.Unmarshal(schema, &cfg.Schema); err != nil {
		return nil, fmt.Errorf("response format: %w", err)
	}
	strict := true
	cfg.Strict = optionalnullable.From(&strict)
	f := components.CreateResponseFormatJSONSchema(
		components.ChatFormatJSONSchemaConfig{JSONSchema: cfg,
			Type: components.ChatFormatJSONSchemaConfigTypeJSONSchema})
	return &f, nil
}

// assistantContent is an assistant message's text, marked as a cache
// point when asked. An assistant message with only tool calls can't
// carry a marker in this format, which has no part to put it on;
// CacheMarks marks the last message, never such a message in the loop.
func assistantContent(m agent.Message) components.ChatAssistantMessageContent {
	if !m.CachePoint {
		return components.CreateChatAssistantMessageContentStr(m.Content)
	}
	return components.CreateChatAssistantMessageContentArrayOfChatContentItems(
		[]components.ChatContentItems{
			components.CreateChatContentItemsText(cachedText(m.Content))})
}

// tag::to-reply[]

// toReply maps the SDK's result onto the agent's reply.
func toReply(r components.ChatResult) (agent.Reply, error) {
	choice := r.Choices[0]
	msg := choice.Message
	var out agent.Reply
	out.Message.Role = "assistant"
	if c, ok := msg.Content.Get(); ok && c != nil && c.Str != nil {
		out.Message.Content = *c.Str
	}
	for _, tc := range msg.ToolCalls {
		out.Message.ToolCalls = append(out.Message.ToolCalls,
			agent.ToolCall{ID: tc.ID, Name: tc.Function.Name,
				Arguments: jsontext.Value(tc.Function.Arguments)})
	}
	if len(msg.ReasoningDetails) > 0 {
		b, err := json.Marshal(msg.ReasoningDetails)
		if err != nil {
			return out, fmt.Errorf("reasoning details: %w", err)
		}
		out.Message.ReasoningDetails = b
	}
	if choice.FinishReason != nil {
		out.FinishReason = string(*choice.FinishReason)
	}
	out.Usage = toUsage(r.Usage)
	return out, nil
}

// end::to-reply[]

func toUsage(u *components.ChatUsage) agent.Usage {
	if u == nil {
		return agent.Usage{}
	}
	out := agent.Usage{PromptTokens: int(u.PromptTokens),
		CompletionTokens: int(u.CompletionTokens)}
	out.Cost, _ = u.Cost.GetOrZero()
	if d, ok := u.PromptTokensDetails.Get(); ok && d != nil {
		if d.CachedTokens != nil {
			out.CachedTokens = int(*d.CachedTokens)
		}
		if d.CacheWriteTokens != nil {
			out.CacheWriteTokens = int(*d.CacheWriteTokens)
		}
	}
	if d, ok := u.CompletionTokensDetails.Get(); ok && d != nil {
		n, _ := d.ReasoningTokens.GetOrZero()
		out.ReasoningTokens = int(n)
	}
	return out
}

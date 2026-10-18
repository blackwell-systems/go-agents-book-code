package agent

// tag::genai[]

// The span and attribute names from OpenTelemetry's semantic
// conventions for generative AI, which are still marked "Development".
// They follow the repository open-telemetry/semantic-conventions-genai
// as of October 2026 (no release tag yet); every name the book sets is
// here, so a rename is a change to this file. Input tokens include
// cached ones, as Usage.PromptTokens does.
const (
	opChat        = "chat"
	opExecuteTool = "execute_tool"
	opInvokeAgent = "invoke_agent"

	attrOperation    = "gen_ai.operation.name"
	attrProvider     = "gen_ai.provider.name"
	attrAgentName    = "gen_ai.agent.name"
	attrRequestModel = "gen_ai.request.model"
	attrResponseID   = "gen_ai.response.id"
	attrInputTokens  = "gen_ai.usage.input_tokens"
	attrOutputTokens = "gen_ai.usage.output_tokens"
	attrCachedTokens = "gen_ai.usage.cache_read.input_tokens"
	attrToolName     = "gen_ai.tool.name"
	attrToolCallID   = "gen_ai.tool.call.id"
	attrErrorType    = "error.type"
)

// end::genai[]

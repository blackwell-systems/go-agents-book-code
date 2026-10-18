package mcp

import "encoding/json/jsontext"

// The _meta keys every request carries in protocol version 2026-07-28.
const (
	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaClient       = "io.modelcontextprotocol/clientInfo"
	metaServer       = "io.modelcontextprotocol/serverInfo"
)

// An Implementation names a client or a server and its version. Neither
// side checks it; it is for logs.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// tag::request-meta[]

// requestMeta is the _meta every request carries, so the server needs
// nothing from earlier requests to answer it. Capabilities is required
// even when empty: this client offers none of the client features
// (roots, sampling, elicitation), so it sends {}.
type requestMeta struct {
	Version      string          `json:"io.modelcontextprotocol/protocolVersion"`
	Capabilities *jsontext.Value `json:"io.modelcontextprotocol/clientCapabilities"`
	Client       *Implementation `json:"io.modelcontextprotocol/clientInfo,omitzero"`
}

// end::request-meta[]

// resultMeta is the _meta a server puts on each result.
type resultMeta struct {
	Server *Implementation `json:"io.modelcontextprotocol/serverInfo,omitzero"`
}

// resultBase is what every result holds. ResultType is "complete" for
// an ordinary result; a server written for an earlier version leaves it
// out, which means the same.
type resultBase struct {
	ResultType string      `json:"resultType,omitzero"`
	Meta       *resultMeta `json:"_meta,omitzero"`
}

// cacheHints say how long a client may reuse a list result.
type cacheHints struct {
	TTL   int    `json:"ttlMs"`      // milliseconds; 0 means don't reuse
	Scope string `json:"cacheScope"` // "public" or "private"
}

// A DiscoverResult is a server's answer to server/discover.
type DiscoverResult struct {
	resultBase
	cacheHints
	SupportedVersions []string       `json:"supportedVersions"`
	Capabilities      jsontext.Value `json:"capabilities"`
	Instructions      string         `json:"instructions,omitzero"`
}

// tag::tool-info[]

// A ToolInfo is one tool as a server describes it in tools/list.
// Everything in it comes from the server, so a client treats it as a
// claim: the description goes into the model's context, and the
// annotations are hints the server may get wrong.
type ToolInfo struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitzero"`
	Description string         `json:"description,omitzero"`
	InputSchema jsontext.Value `json:"inputSchema"`
	Annotations *Annotations   `json:"annotations,omitzero"`
}

// Annotations are a server's hints about a tool's behavior. A hint
// that is left out takes the protocol's default, the cautious one:
// not read-only, destructive, not idempotent.
type Annotations struct {
	ReadOnly    *bool `json:"readOnlyHint,omitzero"`
	Destructive *bool `json:"destructiveHint,omitzero"`
	Idempotent  *bool `json:"idempotentHint,omitzero"`
	OpenWorld   *bool `json:"openWorldHint,omitzero"`
}

// end::tool-info[]

type listToolsResult struct {
	resultBase
	cacheHints
	Tools      []ToolInfo `json:"tools"`
	NextCursor *string    `json:"nextCursor,omitzero"`
}

// tag::call-result[]

// A CallResult is what a tools/call returned. IsError means the tool
// ran and failed, and Content says how, for the model to read; a
// request the server couldn't handle at all is an *Error instead.
type CallResult struct {
	Content           []Content      `json:"content"`
	StructuredContent jsontext.Value `json:"structuredContent,omitzero"`
	IsError           bool           `json:"isError,omitzero"`
}

// A Content is one item of a result: text, or something else, such as
// an image, which this package names but doesn't decode.
type Content struct {
	Type     string `json:"type"` // "text", "image", "audio", ...
	Text     string `json:"text,omitzero"`
	MimeType string `json:"mimeType,omitzero"`
	Data     string `json:"data,omitzero"` // base64: images, audio
	URI      string `json:"uri,omitzero"`  // for resource links
}

// end::call-result[]

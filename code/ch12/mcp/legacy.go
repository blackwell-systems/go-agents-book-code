package mcp

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"
)

// legacyVersions are the earlier protocol versions this client speaks,
// newest first: the ones that open with the initialize handshake.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26",
	"2024-11-05"}

// tag::initialize[]

// initialize opens a connection to a server of an earlier version,
// with the handshake those versions use.
func (c *Client) initialize(ctx context.Context) error {
	var r initializeResult
	err := c.call(ctx, "initialize", struct {
		Version      string         `json:"protocolVersion"`
		Capabilities jsontext.Value `json:"capabilities"`
		Client       Implementation `json:"clientInfo"`
	}{legacyVersions[0], jsontext.Value("{}"), c.info}, &r)
	if err != nil {
		return fmt.Errorf("the server answered neither "+
			"server/discover nor initialize: %w", err)
	}
	if !slices.Contains(legacyVersions, r.Version) {
		return fmt.Errorf("%w: the server chose %s; this client "+
			"speaks %s", ErrVersion, r.Version,
			strings.Join(legacyVersions, ", "))
	}
	c.version = r.Version
	c.legacy.Store(true)
	c.server, c.about = r.Server, r.Instructions
	err = c.c.send(message{Method: "notifications/initialized"})
	if err != nil {
		return fmt.Errorf("initialize: %w: %w", ErrServerGone, err)
	}
	return nil
}

// end::initialize[]

// initializeResult is a server's answer to initialize.
type initializeResult struct {
	resultBase
	Version      string         `json:"protocolVersion"`
	Server       Implementation `json:"serverInfo"`
	Instructions string         `json:"instructions"`
}

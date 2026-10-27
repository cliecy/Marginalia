// Package magpie integrates Marginalia with Magpie
// (https://github.com/yetone/magpie), a local LLM router whose gateway
// speaks the OpenAI API on the loopback interface and whose library hands MCP
// servers to every coding agent on the machine.
//
// The package does two things, both read-only unless the caller explicitly
// asks to register or unregister:
//
//   - detect the gateway (DetectGateway), quickly and without ever blocking
//     the UI for more than DetectTimeout;
//   - drive the magpie command-line tool (FindCLI, RegisterMCP,
//     UnregisterMCP, Library) with timeouts.
package magpie

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Gateway facts. Magpie trusts loopback clients and accepts the literal key
// "magpie" on its OpenAI-compatible API.
const (
	// GatewayURL is the root of Magpie's local gateway.
	GatewayURL = "http://127.0.0.1:3425"
	// BaseURL is the OpenAI-compatible API root to use as AI base URL.
	BaseURL = GatewayURL + "/v1"
	// APIKey is the API key Magpie's gateway accepts.
	APIKey = "magpie"
	// DefaultModel is Magpie's default routing group, preferred when the
	// user switches to Magpie without picking a model.
	DefaultModel = "group/default"
	// InstallURL is where users get Magpie.
	InstallURL = "https://github.com/yetone/magpie"
	// DetectTimeout bounds DetectGateway as a whole.
	DetectTimeout = 600 * time.Millisecond
)

// Gateway describes Magpie's local gateway as GET /api/app/integrations
// reports it.
type Gateway struct {
	// Running is true when a Magpie gateway answered.
	Running bool `json:"running"`
	// URL is the gateway root that was probed.
	URL string `json:"url"`
	// Name is the service name the gateway reported ("magpie").
	Name string `json:"name,omitempty"`
	// Version is Magpie's version ("" when unknown).
	Version string `json:"version"`
	// Models is the number of models the gateway offers; nil when unknown.
	Models *int `json:"models"`
}

// Client talks to one Magpie installation. The zero value probes the
// standard gateway and finds the CLI with FindCLI; tests point it at fakes.
// A Client is safe for concurrent use.
type Client struct {
	// GatewayURL is the gateway root ("" = GatewayURL).
	GatewayURL string
	// HTTPClient is used for gateway probes (nil = a client that never uses
	// an HTTP proxy, since the gateway is on the loopback interface).
	HTTPClient *http.Client
	// DetectTimeout bounds one gateway detection (0 = DetectTimeout).
	DetectTimeout time.Duration
	// CLIPath is the magpie executable ("" = FindCLI at every use).
	CLIPath string
	// CLITimeout bounds mutating CLI commands (0 = DefaultCLITimeout).
	CLITimeout time.Duration
	// StatusTimeout bounds read-only CLI commands (0 = DefaultStatusTimeout).
	StatusTimeout time.Duration
}

// Default is the Client used by the package-level functions.
var Default = &Client{}

// DetectGateway probes Magpie's standard gateway with the Default client.
func DetectGateway(ctx context.Context) Gateway { return Default.Gateway(ctx) }

// IsGatewayURL reports whether raw points at Magpie's gateway: an http URL on
// 127.0.0.1, localhost or ::1 with port 3425 (any path, e.g. /v1).
func IsGatewayURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" {
		return false
	}
	gw, _ := url.Parse(GatewayURL)
	if u.Port() != gw.Port() {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func (c *Client) gatewayURL() string {
	if c.GatewayURL != "" {
		return strings.TrimRight(c.GatewayURL, "/")
	}
	return GatewayURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return loopbackClient
}

// loopbackClient never goes through a proxy: the gateway is local.
var loopbackClient = &http.Client{Transport: &http.Transport{
	Proxy:               nil,
	DialContext:         (&net.Dialer{Timeout: DetectTimeout}).DialContext,
	MaxIdleConnsPerHost: 2,
	IdleConnTimeout:     30 * time.Second,
}}

// Gateway detects the gateway: GET / must answer Magpie's service document
// ({"name":"magpie","version":…,"models":N}); when it does not identify
// itself, GET /v1/models is tried and its model list counted. The whole
// detection takes at most DetectTimeout and never fails: an unreachable
// gateway is reported as not running.
func (c *Client) Gateway(ctx context.Context) Gateway {
	timeout := c.DetectTimeout
	if timeout <= 0 {
		timeout = DetectTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := c.gatewayURL()
	out := Gateway{URL: base}

	var root struct {
		Name    string          `json:"name"`
		Version string          `json:"version"`
		Models  json.RawMessage `json:"models"`
	}
	if c.getJSON(ctx, base+"/", &root) == nil && root.Name != "" {
		if !strings.EqualFold(root.Name, "magpie") {
			return out // another service owns the port
		}
		out.Running, out.Name, out.Version = true, root.Name, root.Version
		out.Models = countModels(root.Models)
		if out.Models != nil {
			return out
		}
	}

	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if c.getJSON(ctx, base+"/v1/models", &list) == nil && list.Data != nil {
		n := len(list.Data)
		out.Running, out.Models = true, &n
	}
	return out
}

// countModels reads the service document's "models": a count or a list.
func countModels(raw json.RawMessage) *int {
	if len(raw) == 0 {
		return nil
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil && n >= 0 {
		v := int(n)
		return &v
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		v := len(list)
		return &v
	}
	return nil
}

// getJSON GETs u (with Magpie's key) and decodes a 200 JSON answer into dst.
func (c *Client) getJSON(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return errors.New("magpie: " + u + " answered HTTP " + strconv.Itoa(resp.StatusCode))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(dst)
}

// OpenAIBaseURL is the OpenAI-compatible API root of the client's gateway
// (BaseURL for the standard gateway).
func (c *Client) OpenAIBaseURL() string { return c.gatewayURL() + "/v1" }

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Text and structured content can both enter the client's context. This allows
// the ADR 0017 summary (32 KiB) with headroom without returning bulk reports.
const mcpMaxResponse = 64 << 10

func runMCP(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api", envDefault("OPENMERGE_API_URL", "http://127.0.0.1:8081"), "status API origin (OPENMERGE_API_URL)")
	tokenFile := flags.String("api-token-file", os.Getenv("OPENMERGE_API_TOKEN_FILE"), "bearer token file (or OPENMERGE_API_TOKEN)")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "mcp accepts flags only")
		return 2
	}
	token, err := status.ReadToken(os.Getenv("OPENMERGE_API_TOKEN"), *tokenFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if token == "" {
		_, _ = fmt.Fprintln(stderr, "mcp requires OPENMERGE_API_TOKEN or --api-token-file/OPENMERGE_API_TOKEN_FILE")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server := newMCPServer(*api, *tokenFile)
	if err := server.Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: mcpOutput{stdout}}); err != nil && ctx.Err() == nil {
		// Protocol errors may contain client inputs. Do not echo them or secrets.
		_, _ = fmt.Fprintln(stderr, "MCP session failed")
		return 1
	}
	return 0
}

type mcpOutput struct{ io.Writer }

func (mcpOutput) Close() error { return nil }

func newMCPServer(api, tokenFile string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "openmerge", Version: version}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{},
		Instructions: "Read-only OpenMerge status API. Start with queues. Supply raw parameter values, including slashes in base branches; the server URL-escapes them. HTTP 202 means computing: wait for Retry-After and repeat the same read. Forge-authenticated comments handle writes.",
	})
	for _, read := range status.Reads() {
		// live is an unauthenticated process probe, not an agent status read.
		if read.Name == "live" {
			continue
		}
		properties := map[string]any{}
		required := []string{}
		for _, parameter := range read.Parameters {
			properties[parameter.Name] = map[string]any{"type": parameter.Type, "description": parameter.Description}
			if parameter.Required {
				required = append(required, parameter.Name)
			}
		}
		server.AddTool(&mcp.Tool{
			Name: read.Name, Description: read.Description,
			InputSchema: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			path, err := mcpReadPath(read, request.Params.Arguments)
			if err != nil {
				return mcpFailure("invalid-request: " + err.Error()), nil
			}
			var diagnostics bytes.Buffer
			response, code := readStatusAPIResponse(ctx, api, path, tokenFile, mcpMaxResponse, true, &diagnostics)
			if code != 0 {
				message := strings.TrimSpace(diagnostics.String())
				if strings.Contains(message, "invalid, oversized or unsupported") {
					message += fmt.Sprintf("; MCP body limit is %d bytes; %s", mcpMaxResponse, mcpNarrowerRead(read))
				}
				return mcpFailure(message), nil
			}
			if !json.Valid(response.body) {
				return mcpFailure("invalid, oversized or unsupported status API response"), nil
			}
			result := &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(response.body)}},
				StructuredContent: json.RawMessage(response.body),
			}
			if response.retryAfter != "" {
				result.Content = append(result.Content, &mcp.TextContent{Text: "Retry-After: " + response.retryAfter + "; wait, then repeat this read."})
			}
			return result, nil
		})
	}
	return server
}

func mcpFailure(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

func mcpNarrowerRead(read status.Read) string {
	for _, parameter := range read.Parameters {
		switch parameter.Name {
		case "detail":
			return "retry with detail=summary"
		case "limit":
			return "retry with a smaller limit and page with after"
		case "events":
			return "retry with events=1, or use change or brief for this PR"
		}
	}
	return "use repository_queues for one repository, or pr, change, or brief for one change; use the API directly for bulk JSON"
}

// Build the escaped path from literal registry segments, never by joining or
// cleaning caller input. In particular, branch slashes remain within a segment.
func mcpReadPath(read status.Read, raw json.RawMessage) (string, error) {
	var arguments map[string]json.RawMessage
	if len(raw) != 0 && json.Unmarshal(raw, &arguments) != nil || string(raw) == "null" {
		return "", errors.New("arguments must be an object")
	}
	values := make(map[string]string, len(arguments))
	query := url.Values{}
	for _, parameter := range read.Parameters {
		value, exists := arguments[parameter.Name]
		delete(arguments, parameter.Name)
		if !exists {
			if parameter.Required {
				return "", fmt.Errorf("missing parameter %s", parameter.Name)
			}
			continue
		}
		var text string
		switch parameter.Type {
		case "string":
			if string(value) == "null" || json.Unmarshal(value, &text) != nil {
				return "", fmt.Errorf("%s must be a string", parameter.Name)
			}
		case "integer", "number":
			var number json.Number
			if json.Unmarshal(value, &number) != nil || string(value) == "null" || len(value) == 0 || value[0] == '"' {
				return "", fmt.Errorf("%s must be a %s", parameter.Name, parameter.Type)
			}
			text = number.String()
			if parameter.Type == "integer" {
				if _, err := strconv.ParseInt(text, 10, 64); err != nil {
					return "", fmt.Errorf("%s must be an integer", parameter.Name)
				}
			}
		case "boolean":
			var boolean bool
			if string(value) == "null" || json.Unmarshal(value, &boolean) != nil {
				return "", fmt.Errorf("%s must be a boolean", parameter.Name)
			}
			text = strconv.FormatBool(boolean)
		default:
			return "", errors.New("unsupported registry parameter type")
		}
		switch parameter.In {
		case "path":
			if text == "" || text == "." || text == ".." {
				return "", fmt.Errorf("%s must be a nonempty path segment other than dot or dot-dot", parameter.Name)
			}
			values[parameter.Name] = url.PathEscape(text)
		case "query":
			query.Set(parameter.Name, text)
		default:
			return "", errors.New("unsupported registry parameter location")
		}
	}
	if len(arguments) != 0 {
		return "", errors.New("unknown parameter")
	}
	segments := strings.Split(read.Path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			value, ok := values[strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")]
			if !ok {
				return "", errors.New("missing registry path parameter")
			}
			segments[i] = value
		}
	}
	path := strings.Join(segments, "/")
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path, nil
}

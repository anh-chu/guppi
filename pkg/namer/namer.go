// Package namer generates short, human-friendly session names by asking a
// custom OpenAI-compatible chat endpoint to synthesize context (working dir,
// branch, agent, user prompt, agent message, or recent shell commands) into a
// concise label.
//
// It is fully optional: if no endpoint is configured the namer is disabled and
// every call is a no-op. Network failures never block the caller — they return
// an error that the caller is expected to log and ignore, keeping the existing
// session name.
package namer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Kind distinguishes the two context profiles the namer understands.
type Kind string

const (
	KindAgent Kind = "agent"
	KindShell Kind = "shell"
	KindGroup Kind = "group"
)

// Context carries everything the namer may use to synthesize a name. Callers
// build it; the namer does not care which Kind it is beyond prompt shaping.
type Context struct {
	Kind       Kind
	Workdir    string
	Branch     string
	Agent      string        // agent type, e.g. "claude" (agent kind only)
	UserPrompt string        // latest user message (agent kind only)
	AgentMsg   string        // latest agent message (agent kind only)
	Commands   []string      // recent shell commands (shell kind only)
	Members    []GroupMember // member sessions (group kind only)
	Current    string        // existing display name for context; the system prompt decides keep vs rename
	Taken      []string      // other sessions' names; the label must be distinct from these
}

// GroupMember carries the current per-session signal the group namer reasons
// over: its label plus whatever metadata exists for that session.
type GroupMember struct {
	Label   string `json:"label"`   // display name or session name
	Agent   string `json:"agent"`   // agent type, e.g. "claude"
	Project string `json:"project"` // project path
	Prompt  string `json:"prompt"`  // user prompt or prompt preview
}

// Config holds the endpoint settings. Endpoint + Model must be non-empty for
// the namer to be enabled. APIKey is optional (some local endpoints need none).
type Config struct {
	Endpoint string // base URL, e.g. https://api.openai.com/v1
	APIKey   string
	Model    string
	Timeout  time.Duration
}

// ConfigFromEnv reads namer configuration from TERMYARD_NAMER_* environment
// variables. Falls back to TERMYARD_OPENAI_* for endpoint/key/model so a single
// OpenAI-compatible config can be shared.
func ConfigFromEnv() Config {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	c := Config{
		Endpoint: get("TERMYARD_NAMER_ENDPOINT", "TERMYARD_OPENAI_ENDPOINT", "TERMYARD_OPENAI_BASE_URL"),
		APIKey:   get("TERMYARD_NAMER_API_KEY", "TERMYARD_OPENAI_API_KEY"),
		Model:    get("TERMYARD_NAMER_MODEL", "TERMYARD_OPENAI_MODEL"),
		Timeout:  8 * time.Second,
	}
	if c.Model == "" && c.Endpoint != "" {
		c.Model = "gpt-4o-mini"
	}
	return c
}

// Configure builds a Config from explicit settings (e.g. user preferences),
// falling back to TERMYARD_NAMER_* / TERMYARD_OPENAI_* environment variables for any
// empty field. If enabled is false it returns a disabled (zero-endpoint)
// Config.
func Configure(enabled bool, endpoint, apiKey, model string) Config {
	if !enabled {
		return Config{}
	}
	env := ConfigFromEnv()
	c := Config{
		Endpoint: firstNonEmpty(endpoint, env.Endpoint),
		APIKey:   firstNonEmpty(apiKey, env.APIKey),
		Model:    firstNonEmpty(model, env.Model),
		Timeout:  8 * time.Second,
	}
	if c.Model == "" && c.Endpoint != "" {
		c.Model = "gpt-4o-mini"
	}
	return c
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Namer talks to the configured endpoint. The zero value (Config{}) yields a
// disabled namer whose Generate always returns ("", ErrDisabled).
type Namer struct {
	cfg    Config
	client *http.Client
}

// ErrDisabled is returned when no endpoint is configured.
var ErrDisabled = fmt.Errorf("namer: no endpoint configured")

// New builds a Namer from cfg.
func New(cfg Config) *Namer {
	if cfg.Timeout == 0 {
		cfg.Timeout = 8 * time.Second
	}
	return &Namer{
		cfg:    cfg,
		client: &http.Client{Timeout: cfg.Timeout},
	}
}

// Enabled reports whether the namer has a usable endpoint + model.
func (n *Namer) Enabled() bool {
	return n != nil && n.cfg.Endpoint != "" && n.cfg.Model != ""
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string              `json:"model"`
	Messages       []chatMessage       `json:"messages"`
	Temperature    float64             `json:"temperature"`
	MaxTokens      int                 `json:"max_tokens"`
	Stream         bool                `json:"stream"`
	ResponseFormat *chatResponseFormat `json:"response_format,omitempty"`
}

type chatResponseFormat struct {
	Type string `json:"type"`
}

// chatResponse covers both the non-streaming shape (choices[].message.content)
// and a single streaming chunk (choices[].delta.content).
type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
		Delta   chatMessage `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

const systemPrompt = `You name terminal sessions. Reply with ONLY a single JSON object of the form {"name": "label"} and nothing else — no markdown, no code fences, no prose, no explanation. The "name" value must be 2-4 words, kebab-case, lowercase, ASCII letters/digits/hyphens only, no quotes or trailing punctuation. Example reply: {"name": "fix-auth-token"}.

Capture this session's specific purpose, not a single transient command. When several sessions share a project, branch, or agent, do NOT name by that shared context — name by what makes THIS session's task different from the others. When both a user request and an agent reply are given, weight the user's request most heavily: the name should describe what the user asked for; treat the agent's reply as secondary supporting context only.

If existing session names are listed, your "name" MUST be distinct from every one of them: use different words, never just a numeric suffix.

If a current name is provided and it still accurately and distinctively describes the work, put that exact name in "name" unchanged. Only rename when the work has clearly moved on.`

// Generate synthesizes a sanitized session name from ctx. Returns ErrDisabled
// if no endpoint is configured. On any network/parse error returns ("", err);
// callers should log and keep the existing name.
func (n *Namer) Generate(ctx context.Context, nc Context) (string, error) {
	if !n.Enabled() {
		return "", ErrDisabled
	}

	user := buildUserPrompt(nc)
	if strings.TrimSpace(user) == "" {
		return "", fmt.Errorf("namer: empty context")
	}

	reqBody := chatRequest{
		Model: n.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: user},
		},
		Temperature:    0.6,
		MaxTokens:      512, // headroom so reasoning models aren't cut off; we take the final line
		Stream:         false,
		ResponseFormat: &chatResponseFormat{Type: "json_object"},
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := strings.TrimRight(n.cfg.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if n.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+n.cfg.APIKey)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("namer: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("namer: endpoint status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	content, err := extractContent(raw)
	if err != nil {
		return "", err
	}

	name := nameFromContent(content)
	if name == "" {
		return "", fmt.Errorf("namer: model returned unusable name (content: %q)", truncate(content, 200))
	}
	return name, nil
}

// extractContent pulls the assistant text out of a response body that may be
// either a single JSON chat completion or a streamed text/event-stream of
// `data: {...}` chunks (some OpenAI-compatible gateways stream by default).
func extractContent(raw []byte) (string, error) {
	trimmed := bytes.TrimSpace(raw)

	// Non-streaming: a single JSON object.
	if bytes.HasPrefix(trimmed, []byte("{")) {
		var out chatResponse
		if err := json.Unmarshal(trimmed, &out); err != nil {
			return "", fmt.Errorf("namer: decode response: %w", err)
		}
		if out.Error != nil {
			return "", fmt.Errorf("namer: endpoint error: %s", out.Error.Message)
		}
		if len(out.Choices) == 0 {
			return "", fmt.Errorf("namer: empty response")
		}
		return out.Choices[0].Message.Content, nil
	}

	// Streaming: accumulate delta (or message) content across `data:` lines.
	var sb strings.Builder
	for _, line := range strings.Split(string(trimmed), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk chatResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if c := chunk.Choices[0].Delta.Content; c != "" {
			sb.WriteString(c)
		} else if c := chunk.Choices[0].Message.Content; c != "" {
			sb.WriteString(c)
		}
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("namer: empty streamed response")
	}
	return sb.String(), nil
}

func buildUserPrompt(nc Context) string {
	var b strings.Builder
	if nc.Current != "" {
		fmt.Fprintf(&b, "Current name: %s\n", nc.Current)
	}
	if nc.Workdir != "" {
		fmt.Fprintf(&b, "Working directory: %s\n", nc.Workdir)
	}
	if nc.Branch != "" {
		fmt.Fprintf(&b, "Git branch: %s\n", nc.Branch)
	}
	switch nc.Kind {
	case KindAgent:
		if nc.Agent != "" {
			fmt.Fprintf(&b, "Coding agent: %s\n", nc.Agent)
		}
		if nc.UserPrompt != "" {
			fmt.Fprintf(&b, "User asked (PRIMARY signal — base the name on this): %s\n", truncate(nc.UserPrompt, 900))
		}
		if nc.AgentMsg != "" {
			fmt.Fprintf(&b, "Agent replied (secondary context only): %s\n", truncate(nc.AgentMsg, 300))
		}
	case KindShell:
		if len(nc.Commands) > 0 {
			b.WriteString("Recent shell commands:\n")
			for _, c := range nc.Commands {
				c = strings.TrimSpace(c)
				if c != "" {
					fmt.Fprintf(&b, "  %s\n", truncate(c, 200))
				}
			}
		}
	case KindGroup:
		if len(nc.Members) > 0 {
			b.WriteString("Sessions in this group:\n")
			for _, m := range nc.Members {
				label := strings.TrimSpace(m.Label)
				if label == "" {
					continue
				}
				fmt.Fprintf(&b, "  - %s\n", truncate(label, 120))
				if a := strings.TrimSpace(m.Agent); a != "" {
					fmt.Fprintf(&b, "      agent: %s\n", a)
				}
				if p := strings.TrimSpace(m.Project); p != "" {
					fmt.Fprintf(&b, "      project: %s\n", truncate(p, 120))
				}
				if pr := strings.TrimSpace(m.Prompt); pr != "" {
					fmt.Fprintf(&b, "      task: %s\n", truncate(pr, 200))
				}
			}
		}
		b.WriteString("\nName this group by what its sessions have in common. If they share a project path, agent, or task, make that commonality the name. Fall back to the broadest shared theme only when there is no obvious common attribute.")
		return b.String()
	}
	if len(nc.Taken) > 0 {
		b.WriteString("Other existing session names (your label must be distinct from all of these):\n")
		for _, t := range nc.Taken {
			if t = strings.TrimSpace(t); t != "" {
				fmt.Fprintf(&b, "  - %s\n", truncate(t, 60))
			}
		}
	}
	b.WriteString("\nName this session.")
	return b.String()
}

// lastLine returns the last non-empty line of s, so reasoning models may emit
// thinking before the final label and we still pick the label.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

// extractLabel picks the final label from a model reply. Models are asked to
// end with the bare label, but reasoning/markdown models often trail a closing
// code fence, a thinking tag, or other punctuation-only line whose sanitized
// form is empty — lastLine alone would return that and then Sanitize to "",
// dropping the real label sitting on the line above. Scan bottom-up and return
// the last non-empty line that sanitizes to a name; fall back to sanitizing
// the whole reply so a label embedded in prose still yields something usable.
func extractLabel(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if name := Sanitize(t); name != "" {
			return name
		}
	}
	return Sanitize(strings.TrimSpace(s))
}

// nameFromContent extracts the label from a model reply. Structured-output
// models/gateways return JSON {"name":"..."}; non-structured ones ignore the
// JSON ask and emit a bare label, prose, or a code-fenced reply. Try JSON
// first (strict, then any embedded {...} object), then fall back to the
// label-line scanner so both shapes work.
func nameFromContent(content string) string {
	if name := jsonName(content); name != "" {
		return name
	}
	return extractLabel(content)
}

// jsonName decodes {"name":"..."} from a strict JSON reply, then tries the
// last {...} object embedded in prose/fences for replies that wrap or prefix
// the JSON with text (common when response_format is ignored).
func jsonName(content string) string {
	if name := decodeJSONName(strings.TrimSpace(content)); name != "" {
		return name
	}
	for i := len(content); i > 0; {
		end := strings.LastIndex(content[:i], "}")
		if end < 0 {
			break
		}
		start := strings.LastIndex(content[:end], "{")
		if start < 0 {
			break
		}
		if name := decodeJSONName(content[start : end+1]); name != "" {
			return name
		}
		i = start
	}
	return ""
}

func decodeJSONName(s string) string {
	var v struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return ""
	}
	return Sanitize(v.Name)
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

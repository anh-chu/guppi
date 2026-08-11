package notify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"

	"github.com/anh-chu/termyard/pkg/auth"
	"github.com/anh-chu/termyard/pkg/common"
	"github.com/anh-chu/termyard/pkg/socket"
	"github.com/anh-chu/termyard/pkg/toolevents"
)

// stdinEvent represents the JSON payload that agent hooks pass via stdin.
// Supports Codex hook events (SessionStart, Stop), Claude UserPromptSubmit,
// and generic explicit values from other agents.
type stdinEvent struct {
	HookEventName string `json:"hook_event_name"`
	// Tool name for activity labels
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	// Claude Stop / Codex agent-turn-complete
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
	// Claude Code Stop hook transcript path
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Claude UserPromptSubmit fields
	Prompt *string `json:"prompt,omitempty"`
	// Generic fields for agents that send explicit values (e.g. Pi extension)
	SessionID    string `json:"session_id,omitempty"`
	UserPrompt   string `json:"user_prompt,omitempty"`
	AgentMessage string `json:"agent_message,omitempty"`
}

// stdinResult holds the parsed fields from a stdin hook event.
type stdinResult struct {
	Tool, Status, Message, UserPrompt, AgentMessage, AgentSessionID, ToolName string
	Files                                                                      []string
}

// toolNameToActivity maps an agent tool name to a human-readable activity label.
func toolNameToActivity(toolName string) string {
	switch strings.ToLower(toolName) {
	// File reads
	case "read", "read_file", "readfile", "cat", "view", "open_file":
		return "reading files"

	// File writes / edits
	case "write", "write_file", "writefile", "edit", "multiedit", "patch", "insert", "str_replace", "str_replace_editor":
		return "editing files"

	// File listings / discovery
	case "ls", "list", "list_dir", "list_directory", "glob":
		return "listing files"

	// Search
	case "grep", "grep_search", "rg", "ripgrep", "find", "search", "websearch", "web_search", "semantic_search":
		return "searching"

	// Command execution
	case "bash", "shell", "computer", "terminal", "run_command", "execute", "exec":
		return "running commands"

	// Web access
	case "fetch", "web_fetch", "curl", "http", "browser", "navigate":
		return "fetching web data"

	// Code analysis
	case "analyze", "lint", "type_check", "diagnostics":
		return "analyzing code"

	// Agent-specific
	case "task":
		return "running subagent"
	default:
		if toolNameIsInteractiveWait(toolName) {
			return "waiting for input"
		}
		return ""
	}
}

// toolNameIsInteractiveWait reports whether a tool blocks the turn on user
// input. When such a tool starts (PreToolUse), the agent is genuinely waiting
// for the user, so the event status must be "waiting" rather than "active".
// Kept in sync with the ask_user/ask_question cases in toolNameToActivity.
func toolNameIsInteractiveWait(toolName string) bool {
	// Normalize: lowercase and strip separators so ask_user / ask-user /
	// askUser all collapse to the same key. Whole-name match only — substring
	// matching is deliberately avoided because tool names like "task" contain
	// "ask" and would false-trigger on every subagent spawn.
	norm := strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(toolName))
	switch norm {
	case "askuser", "askquestion", "askuserquestion", "question", "userinput", "requestinput":
		return true
	default:
		return false
	}
}

// parsePatchFiles extracts file paths from an apply_patch envelope's marker
// lines (used by Codex and OpenCode, which both use apply_patch-style patches
// for file edits). Only exact "*** " prefixed lines at column 0 are treated
// as markers, so diff hunk/content lines (which start with @@, +, -, or space)
// are never matched.
func parsePatchFiles(patch string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "*** ") {
			continue
		}
		rest := line[len("*** "):]
		var path string
		switch {
		case strings.HasPrefix(rest, "Add File:"):
			path = strings.TrimSpace(rest[len("Add File:"):])
		case strings.HasPrefix(rest, "Update File:"):
			path = strings.TrimSpace(rest[len("Update File:"):])
		case strings.HasPrefix(rest, "Move to:"):
			path = strings.TrimSpace(rest[len("Move to:"):])
		default:
			continue
		}
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

// extractToolInputPaths pulls file path(s) out of a PostToolUse hook's
// tool_input payload. Handles Claude/Pi/OpenCode's single-file "file_path"/
// "path" field, and Codex/OpenCode's apply_patch "command"/patch-text field
// (multi-file capable).
func extractToolInputPaths(toolName string, raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	if strings.ToLower(toolName) == "apply_patch" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Command == "" {
			return nil
		}
		return parsePatchFiles(in.Command)
	}
	var in struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	if in.FilePath != "" {
		return []string{in.FilePath}
	}
	if in.Path != "" {
		return []string{in.Path}
	}
	return nil
}

// chooseAgentSessionID applies precedence: flag > event-data > stdin.
func chooseAgentSessionID(flagValue, eventValue, stdinValue string) string {
	if strings.TrimSpace(flagValue) != "" {
		return strings.TrimSpace(flagValue)
	}
	if strings.TrimSpace(eventValue) != "" {
		return strings.TrimSpace(eventValue)
	}
	return strings.TrimSpace(stdinValue)
}

// parseStdinEvent reads JSON from stdin and maps it to a stdinResult.
func parseStdinEvent(tool string) (stdinResult, error) {
	data, readErr := io.ReadAll(os.Stdin)
	if readErr != nil {
		return stdinResult{}, fmt.Errorf("failed to read stdin: %w", readErr)
	}
	if len(data) == 0 {
		return stdinResult{}, fmt.Errorf("no input on stdin")
	}

	var evt stdinEvent
	if jsonErr := json.Unmarshal(data, &evt); jsonErr != nil {
		return stdinResult{}, fmt.Errorf("failed to parse stdin JSON: %w", jsonErr)
	}

	var (
		status         = "active"
		message        = "Working"
		userPrompt     string
		agentMessage   string
		agentSessionID string
		files          []string
	)

	switch evt.HookEventName {
	case "SessionStart":
		// Session just launched, no work in flight. Emit completed so we don't
		// open a working turn; the process-tree detector marks the agent present
		// (idle). Empty message avoids clobbering the sidebar prompt preview.
		status = "completed"
		message = ""
	case "PreToolUse", "preToolUse":
		status = "active"
		if activity := toolNameToActivity(evt.ToolName); activity != "" {
			message = activity
		} else {
			message = "Working"
		}
		// An interactive ask tool blocks the turn on the user. Surface it as
		// "waiting" so the sidebar flags it for attention; a later PostToolUse
		// (user answered) or agent_end emits active/completed and clears it.
		if toolNameIsInteractiveWait(evt.ToolName) {
			status = "waiting"
			message = "Needs input"
		}
	case "PostToolUse", "postToolUse":
		status = "active"
		if activity := toolNameToActivity(evt.ToolName); activity != "" {
			message = activity
		} else {
			message = "Working"
		}
		files = extractToolInputPaths(evt.ToolName, evt.ToolInput)
	case "Stop":
		status = "completed"
		message = "Task complete"
		if evt.LastAssistantMessage != "" {
			message = truncate(evt.LastAssistantMessage, 300)
			agentMessage = message
		} else if evt.TranscriptPath != "" {
			if msg := extractLastAssistantMessage(evt.TranscriptPath); msg != "" {
				message = truncate(msg, 300)
				agentMessage = message
			}
		}
	case "UserPromptSubmit":
		status = "active"
		message = "Thinking"
		if evt.Prompt != nil && *evt.Prompt != "" {
			t := *evt.Prompt
			if len(t) > 200 {
				t = t[:200]
			}
			userPrompt = t
		}
	default:
		if evt.HookEventName != "" {
			message = evt.HookEventName
		}
	}

	// Generic fields override event-derived values (used by Pi and future agents)
	if evt.SessionID != "" {
		agentSessionID = evt.SessionID
	}
	if evt.UserPrompt != "" {
		userPrompt = evt.UserPrompt
	}
	if evt.AgentMessage != "" {
		agentMessage = evt.AgentMessage
	}

	return stdinResult{
		Tool:           tool,
		Status:         status,
		Message:        message,
		UserPrompt:     userPrompt,
		AgentMessage:   agentMessage,
		AgentSessionID: agentSessionID,
		ToolName:       evt.ToolName,
		Files:          files,
	}, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// extractLastAssistantMessage reads Claude Code transcript JSONL and returns last assistant content.
func extractLastAssistantMessage(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lastAssistant string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		role, ok := entry["role"].(string)
		if !ok || role != "assistant" {
			continue
		}

		if content, ok := entry["content"].(string); ok {
			lastAssistant = content
			continue
		}

		contentArray, ok := entry["content"].([]interface{})
		if !ok {
			continue
		}

		var texts []string
		for _, item := range contentArray {
			obj, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if text, ok := obj["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
		if len(texts) > 0 {
			lastAssistant = strings.Join(texts, " ")
		}
	}

	if err := scanner.Err(); err != nil {
		return ""
	}
	return lastAssistant
}

// codexEvent represents the JSON payload Codex passes as argv[1] to notify
// commands. See: https://developers.openai.com/codex/config-advanced/#notifications
type codexEvent struct {
	Type                 string `json:"type"`
	ThreadID             string `json:"thread-id"`
	TurnID               string `json:"turn-id"`
	CWD                  string `json:"cwd"`
	LastAssistantMessage string `json:"last-assistant-message"`
}

func parseCodexEvent(data string) (*codexEvent, error) {
	var evt codexEvent
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return nil, fmt.Errorf("failed to parse codex event JSON: %w", err)
	}
	return &evt, nil
}

// parseEventData parses a JSON string passed via --event-data (argv) and maps
// it to status/message/agentMessage based on the tool type.
// Returns (status, message, agentMessage, error).
func parseEventData(tool, data string) (string, string, string, error) {
	switch tool {
	case "codex":
		return parseCodexEventData(data)
	default:
		// Generic: try to extract a status and message from the JSON
		var generic map[string]interface{}
		if err := json.Unmarshal([]byte(data), &generic); err != nil {
			return "", "", "", fmt.Errorf("failed to parse event JSON: %w", err)
		}
		return "active", "Event received", "", nil
	}
}

// parseCodexEventData parses Codex's notification JSON.
// Currently only "agent-turn-complete" is emitted.
// Returns (status, message, agentMessage, error).
func parseCodexEventData(data string) (string, string, string, error) {
	evt, err := parseCodexEvent(data)
	if err != nil {
		return "", "", "", err
	}

	switch evt.Type {
	case "agent-turn-complete":
		message := "Task complete"
		agentMessage := ""
		if evt.LastAssistantMessage != "" {
			msg := evt.LastAssistantMessage
			if len(msg) > 300 {
				msg = msg[:300] + "..."
			}
			agentMessage = msg
			message = msg
		}
		return "completed", message, agentMessage, nil
	default:
		// Unknown event type — treat as active
		message := evt.Type
		if message == "" {
			message = "Event received"
		}
		return "active", message, "", nil
	}
}

// detectSessionContext auto-detects the current session, window, and pane
// from the environment. Checks TERMYARD_SESSION/TERMYARD_PANE first (daemon
// sessions), then falls back to TMUX_PANE + tmux queries for legacy setups.
// Returns session name, window index, pane ID.
func detectSessionContext() (string, int, string) {
	// Daemon sessions set TERMYARD_SESSION and TERMYARD_PANE.
	if tySession := os.Getenv("TERMYARD_SESSION"); tySession != "" {
		tyPane := os.Getenv("TERMYARD_PANE")
		if tyPane == "" {
			tyPane = tySession + ":0.0"
		}
		return tySession, 0, tyPane
	}

	// Legacy tmux fallback: use TMUX_PANE to query context.
	paneID := os.Getenv("TMUX_PANE")
	if paneID != "" {
		session, window := queryPaneContext(paneID)
		if session != "" {
			return session, window, paneID
		}
	}

	// Last resort: tmux display-message (returns active pane context).
	session, _ := tmuxQuery("#{session_name}")
	winStr, _ := tmuxQuery("#{window_index}")
	winIdx, _ := strconv.Atoi(winStr)

	return strings.TrimSpace(session), winIdx, strings.TrimSpace(paneID)
}

// queryPaneContext gets the session name and window index for a specific pane ID
func queryPaneContext(paneID string) (string, int) {
	cmd := exec.Command("tmux", "display-message", "-t", paneID, "-p", "#{session_name}\t#{window_index}")
	out, err := cmd.Output()
	if err != nil {
		return "", 0
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(parts) != 2 {
		return "", 0
	}
	winIdx, _ := strconv.Atoi(parts[1])
	return parts[0], winIdx
}

func postViaSocket(socketPath string, body []byte) (*http.Response, error) {
	client := &http.Client{
		Timeout: 1 * time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}
	return client.Post("http://localhost/api/tool-event", "application/json", bytes.NewReader(body))
}

func tmuxQuery(format string) (string, error) {
	cmd := exec.Command("tmux", "display-message", "-p", format)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func Execute(ctx context.Context, c *cli.Command) error {
	tool := c.String("tool")
	status := c.String("status")
	message := c.String("message")
	userPrompt := c.String("user-prompt")
	agentMessage := c.String("agent-message")
	session := c.String("session")
	window := int(c.Int("window"))
	pane := c.String("pane")
	serverURL := c.String("server")
	cwd := ""
	agentSessionID := c.String("agent-session-id")
	toolName := "" // actual tool name from hook events (e.g. "write", "edit")
	var stdinFiles []string

	log := logrus.WithField("component", "notify")

	log.WithFields(logrus.Fields{
		"tool": tool, "status": status,
		"event-data-set": c.IsSet("event-data"), "stdin-set": c.Bool("stdin"),
	}).Trace("notify command invoked")

	// If --event-data is set, parse the JSON blob (passed as argv by agents like Codex)
	if c.IsSet("event-data") {
		rawData := c.String("event-data")

		evtStatus, evtMessage, evtAgentMsg, err := parseEventData(tool, rawData)
		if err != nil {
			return fmt.Errorf("event-data parse: %w", err)
		}

		if !c.IsSet("status") {
			status = evtStatus
		}
		if !c.IsSet("message") {
			message = evtMessage
		}
		if agentMessage == "" && evtAgentMsg != "" {
			agentMessage = evtAgentMsg
		}
		if tool == "codex" {
			codexEvt, err := parseCodexEvent(rawData)
			if err != nil {
				return fmt.Errorf("event-data parse: %w", err)
			}
			if agentSessionID == "" {
				agentSessionID = codexEvt.ThreadID
			}
			cwd = codexEvt.CWD
		}
	}

	// If --stdin is set, read event JSON from stdin and derive status/message/task/userPrompt/agentMessage
	if c.Bool("stdin") {
		log.Trace("reading event from stdin")
		res, err := parseStdinEvent(tool)
		if err != nil {
			return fmt.Errorf("stdin parse: %w", err)
		}
		tool = res.Tool
		stdinFiles = res.Files
		toolName = res.ToolName
		if !c.IsSet("status") {
			status = res.Status
		}
		if !c.IsSet("message") {
			message = res.Message
		}
		if userPrompt == "" && res.UserPrompt != "" {
			userPrompt = res.UserPrompt
		}
		if agentMessage == "" && res.AgentMessage != "" {
			agentMessage = res.AgentMessage
		}
		agentSessionID = chooseAgentSessionID(c.String("agent-session-id"), agentSessionID, res.AgentSessionID)
	}

	if status == "" {
		return fmt.Errorf("--status is required (or use --event-data/--stdin to read from agent hook input)")
	}

	// Auto-detect session context if not provided
	if session == "" || pane == "" {
		log.WithField("TERMYARD_SESSION", os.Getenv("TERMYARD_SESSION")).Trace("auto-detecting session context")
		autoSession, autoWindow, autoPane := detectSessionContext()
		log.WithFields(logrus.Fields{
			"auto_session": autoSession, "auto_window": autoWindow, "auto_pane": autoPane,
		}).Trace("session context detected")

		if session == "" {
			session = autoSession
		}
		if !c.IsSet("window") {
			window = autoWindow
		}
		if pane == "" {
			pane = autoPane
		}
	}

	if session == "" {
		return fmt.Errorf("could not detect session; pass --session explicitly")
	}

	evt := &toolevents.Event{
		Tool:           toolevents.Tool(tool),
		ToolName:       toolName,
		Status:         toolevents.Status(status),
		Session:        session,
		Window:         window,
		Pane:           pane,
		Message:        message,
		CWD:            cwd,
		AgentSessionID: agentSessionID,
		UserPrompt:     userPrompt,
		AgentMessage:   agentMessage,
		Files:          stdinFiles,
	}

	body, err := json.Marshal(evt)
	if err != nil {
		return err
	}

	log.WithFields(logrus.Fields{
		"tool": tool, "status": status, "session": session,
		"window": window, "pane": pane,
	}).Trace("sending event")

	socketPath := c.String("socket")
	if socketPath == "" {
		socketPath = socket.DefaultPath()
	}

	var resp *http.Response

	// Try Unix socket first unless --server was explicitly set
	if !c.IsSet("server") {
		log.WithField("socket", socketPath).Trace("attempting unix socket")
		resp, err = postViaSocket(socketPath, body)
		if err != nil {
			log.WithError(err).Trace("unix socket failed, will try HTTP")
		} else {
			log.WithField("status_code", resp.StatusCode).Trace("unix socket response")
		}
	}

	// Fall back to HTTP
	if resp == nil {
		url := fmt.Sprintf("%s/api/tool-event", serverURL)
		log.WithField("url", url).Trace("sending via HTTP")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("failed to build notify request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token, err := auth.ReadNotifyToken(); err == nil && token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		httpClient := &http.Client{Timeout: 1 * time.Second}
		resp, err = httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to notify termyard: %w", err)
		}
		log.WithField("status_code", resp.StatusCode).Trace("HTTP response")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("termyard returned status %d", resp.StatusCode)
	}

	log.WithFields(logrus.Fields{
		"tool": tool, "status": status, "session": session,
		"window": window, "pane": pane,
	}).Debug("notification sent")

	return nil
}

func init() {
	flags := []cli.Flag{
		&cli.StringFlag{
			Name:     "tool",
			Aliases:  []string{"t"},
			Usage:    "tool name: claude, codex, opencode",
			Required: true,
		},
		&cli.StringFlag{
			Name:    "status",
			Aliases: []string{"s"},
			Usage:   "status: active, waiting, completed, error",
		},
		&cli.StringFlag{
			Name:    "message",
			Aliases: []string{"m"},
			Usage:   "human-readable message",
		},
		&cli.StringFlag{
			Name:  "event-data",
			Usage: "agent event JSON passed as argument (used by Codex notify hook)",
		},
		&cli.BoolFlag{
			Name:  "stdin",
			Usage: "read hook event JSON from stdin (for agent hooks that pass context via stdin)",
		},
		&cli.StringFlag{
			Name:  "user-prompt",
			Usage: "user's first message for this session (set once, never overwritten)",
		},
		&cli.StringFlag{
			Name:  "agent-message",
			Usage: "agent's last response message (updated each turn)",
		},
		&cli.StringFlag{
			Name:  "agent-session-id",
			Usage: "agent session or thread id for recovery resume",
		},
		&cli.StringFlag{
			Name:  "session",
			Usage: "session name (auto-detected if omitted)",
		},
		&cli.IntFlag{
			Name:  "window",
			Usage: "window index (auto-detected if omitted)",
		},
		&cli.StringFlag{
			Name:  "pane",
			Usage: "pane ID (auto-detected if omitted)",
		},
		&cli.StringFlag{
			Name:    "server",
			Usage:   "termyard server URL (HTTP fallback)",
			Sources: cli.EnvVars("TERMYARD_URL"),
			Value:   "http://localhost:7654",
		},
		&cli.StringFlag{
			Name:    "socket",
			Usage:   "path to termyard unix socket (auto-detected if omitted)",
			Sources: cli.EnvVars("TERMYARD_SOCKET"),
		},
	}

	cmd := &cli.Command{
		Name:  "notify",
		Usage: "send an agent hook event to termyard",
		Description: `Notify termyard of AI tool activity. Used by agent hooks.

Examples:
  termyard notify -t claude -s waiting -m "Needs approval"
  termyard notify -t codex -s active
  termyard notify -t claude -s completed

The session, window, and pane are auto-detected when run inside a session.`,
		Flags:  flags,
		Action: Execute,
	}

	common.RegisterCommand(cmd)
}

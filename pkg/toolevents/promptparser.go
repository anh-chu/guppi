package toolevents

import (
	"regexp"
	"strings"
)

// PromptResult describes whether captured pane content appears to be
// prompting the user for input.
type PromptResult struct {
	IsPrompt bool
	Message  string
}

// Patterns that match structured approval prompts (question + options).
// These require specific formatting, not just keyword presence.
var (
	// Matches explicit yes/no style prompts with delimiters
	yesNoPrompt = regexp.MustCompile(`(?i)\(y/n\)|\[Y/n\]|\[y/N\]|\byes\s*/\s*no\b`)

	// Matches parenthesized single-key options like (y) (n) (a) (p) (esc)
	keyOption = regexp.MustCompile(`\([a-zA-Z]\)|\(esc\)|\(enter\)`)

	// Matches numbered option lists (e.g. "1.", "2)", "3.")
	numberedOption = regexp.MustCompile(`^\s*\d+[.)]\s+\S`)

	// Matches lines ending with common input prompt characters
	inputPromptSuffix = regexp.MustCompile(`[?:>]\s*$`)

	// Matches common shell prompt lines (should NOT trigger detection)
	shellPrompt = regexp.MustCompile(
		`^[\s❯\$#%]*$|` + // Bare prompt chars (❯, $, etc.)
			`^\S+@\S+[:\$#%]\s*$|` + // user@host:~ $
			`^[~/][\w/~.-]*[\$#%]\s*$`, // /path/to/dir$
	)

	// Matches "press enter" / "continue?" style prompts
	continuePrompt = regexp.MustCompile(`(?i)(press enter|hit enter|press return|continue\??|proceed\??)\s*$`)
)

// DetectPrompt inspects the last ~10 lines of pane content and determines
// whether the agent appears to be prompting for user input.
//
// Detection requires structured evidence — a single keyword match is not
// enough. We look for combinations like: a question line + numbered options,
// explicit (y/n) delimiters, or parenthesized key options like (a) (d).
func DetectPrompt(content string) PromptResult {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	// Only inspect the last 10 non-empty lines
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < 10; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed != "" {
			tail = append([]string{trimmed}, tail...)
		}
	}

	if len(tail) == 0 {
		return PromptResult{}
	}

	block := strings.Join(tail, "\n")
	lastLine := tail[len(tail)-1]

	// 1. Explicit yes/no prompts — high confidence, standalone match
	if yesNoPrompt.MatchString(block) {
		return PromptResult{IsPrompt: true, Message: "Approval prompt detected"}
	}

	// 2. Parenthesized key options like (y) (n) (a) (p) (esc)
	//    Require at least 2 distinct options to avoid false positives
	keyMatches := keyOption.FindAllString(block, -1)
	if len(uniqueStrings(keyMatches)) >= 2 {
		return PromptResult{IsPrompt: true, Message: "Approval prompt detected"}
	}

	// 3. Numbered option list + prompt-like last line
	hasNumberedList := false
	numberedCount := 0
	for _, line := range tail {
		if numberedOption.MatchString(line) {
			hasNumberedList = true
			numberedCount++
		}
	}
	if hasNumberedList && numberedCount >= 2 && inputPromptSuffix.MatchString(lastLine) {
		return PromptResult{IsPrompt: true, Message: "Selection prompt detected"}
	}

	// 4. "Press enter" / "continue?" style prompts
	if continuePrompt.MatchString(lastLine) {
		return PromptResult{IsPrompt: true, Message: "Input prompt detected"}
	}

	// 5. Short last line ending with ? : > (but not a shell prompt)
	//    Only match if the line contains actual words (not just symbols)
	if inputPromptSuffix.MatchString(lastLine) && !shellPrompt.MatchString(lastLine) {
		if len(lastLine) < 80 && len(lastLine) > 3 && containsWord(lastLine) {
			return PromptResult{IsPrompt: true, Message: "Input prompt detected"}
		}
	}

	return PromptResult{}
}

// uniqueStrings returns deduplicated entries from a string slice.
func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		lower := strings.ToLower(s)
		if !seen[lower] {
			seen[lower] = true
			out = append(out, s)
		}
	}
	return out
}

// containsWord returns true if the string contains at least one alphabetic word.
var wordPattern = regexp.MustCompile(`[a-zA-Z]{2,}`)

func containsWord(s string) bool {
	return wordPattern.MatchString(s)
}

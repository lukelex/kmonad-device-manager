package manager

import (
	"regexp"
	"strconv"
	"unicode/utf8"
)

const submittedBehaviorScope = "submitted_behavior"

// behaviorSourceMap deliberately maps only individual deflayer assignments.
// Mapping an entire submitted layer would let a structural parser error be
// incorrectly attributed to a key assignment.
type behaviorSourceMap struct {
	behavior       []byte
	candidateStart int
	assignments    []byteSpan
}

type byteSpan struct{ start, end int }

func newBehaviorSourceMap(behavior []byte, candidateStart int) behaviorSourceMap {
	if !utf8.Valid(behavior) {
		return behaviorSourceMap{}
	}
	forms, ok := parseBehaviorForms(behavior)
	if !ok {
		return behaviorSourceMap{}
	}
	assignments := make([]byteSpan, 0)
	for _, form := range forms {
		if form.atom != "" || len(form.children) < 3 || form.children[0].atom != "deflayer" {
			continue
		}
		for _, assignment := range form.children[2:] {
			assignments = append(assignments, byteSpan{start: assignment.start, end: assignment.end})
		}
	}
	return behaviorSourceMap{behavior: behavior, candidateStart: candidateStart, assignments: assignments}
}

// candidateDiagnosticLocation maps a validator range only when the complete
// range belongs to exactly one submitted deflayer assignment.
func (m behaviorSourceMap) candidateDiagnosticLocation(candidate []byte, diagnostic validatorRange) *DiagnosticLocation {
	if len(m.assignments) == 0 {
		return nil
	}
	start, ok := lineColumnOffset(candidate, diagnostic.startLine, diagnostic.startColumn)
	if !ok {
		return nil
	}
	endLine, endColumn := diagnostic.endLine, diagnostic.endColumn
	if endLine == 0 {
		end, ok := nextRuneOffset(candidate, start)
		if !ok {
			return nil
		}
		endLine, endColumn = offsetLineColumn(candidate, end)
	}
	end, ok := lineColumnOffset(candidate, endLine, endColumn)
	if !ok || start >= end || start < m.candidateStart {
		return nil
	}
	sourceStart, sourceEnd := start-m.candidateStart, end-m.candidateStart
	for _, assignment := range m.assignments {
		if sourceStart >= assignment.start && sourceEnd <= assignment.end {
			startLine, startColumn := offsetLineColumn(m.behavior, sourceStart)
			endLine, endColumn := offsetLineColumn(m.behavior, sourceEnd)
			return &DiagnosticLocation{
				Scope: submittedBehaviorScope, StartLine: startLine, StartColumn: startColumn,
				EndLine: endLine, EndColumn: endColumn,
			}
		}
	}
	return nil
}

type behaviorForm struct {
	start, end int
	atom       string
	children   []behaviorForm
}

func parseBehaviorForms(data []byte) ([]behaviorForm, bool) {
	var forms []behaviorForm
	for offset := skipBehaviorSpace(data, 0); offset < len(data); offset = skipBehaviorSpace(data, offset) {
		form, next, ok := parseBehaviorForm(data, offset)
		if !ok {
			return nil, false
		}
		forms = append(forms, form)
		offset = next
	}
	return forms, true
}

func parseBehaviorForm(data []byte, offset int) (behaviorForm, int, bool) {
	if offset >= len(data) {
		return behaviorForm{}, offset, false
	}
	start := offset
	if data[offset] != '(' {
		if data[offset] == ')' {
			return behaviorForm{}, offset, false
		}
		if data[offset] == '"' {
			offset++
			for offset < len(data) {
				if data[offset] == '\\' {
					offset += 2
					continue
				}
				if offset < len(data) && data[offset] == '"' {
					return behaviorForm{start: start, end: offset + 1}, offset + 1, true
				}
				offset++
			}
			return behaviorForm{}, offset, false
		}
		for offset < len(data) && !isBehaviorDelimiter(data[offset]) {
			offset++
		}
		if start == offset {
			return behaviorForm{}, offset, false
		}
		return behaviorForm{start: start, end: offset, atom: string(data[start:offset])}, offset, true
	}
	offset++
	children := make([]behaviorForm, 0)
	for {
		offset = skipBehaviorSpace(data, offset)
		if offset >= len(data) {
			return behaviorForm{}, offset, false
		}
		if data[offset] == ')' {
			return behaviorForm{start: start, end: offset + 1, children: children}, offset + 1, true
		}
		child, next, ok := parseBehaviorForm(data, offset)
		if !ok {
			return behaviorForm{}, offset, false
		}
		children = append(children, child)
		offset = next
	}
}

func skipBehaviorSpace(data []byte, offset int) int {
	for offset < len(data) {
		switch data[offset] {
		case ' ', '\t', '\r', '\n':
			offset++
		case ';':
			for offset < len(data) && data[offset] != '\n' {
				offset++
			}
		default:
			return offset
		}
	}
	return offset
}

func isBehaviorDelimiter(value byte) bool {
	return value == '(' || value == ')' || value == ';' || value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

type validatorRange struct {
	startLine, startColumn int
	endLine, endColumn     int
}

var (
	validatorLongRange            = regexp.MustCompile(`(?i)\bline\s+(\d+)\s*(?:,|:)\s*(?:column|col)\s+(\d+)(?:\s*(?:-|–|to)\s*(?:line\s+(\d+)\s*(?:,|:)\s*)?(?:column|col)\s+(\d+))?`)
	validatorShortErrorRange      = regexp.MustCompile(`(?i)\b(?:parse\s+)?error\b[^\r\n]*?\b(?:at\s+)?(\d+)\s*:\s*(\d+)(?:\s*(?:-|–)\s*(\d+)\s*:\s*(\d+))?\b`)
	validatorShortStandaloneRange = regexp.MustCompile(`(?m)^\s*(\d+)\s*:\s*(\d+)(?:\s*(?:-|–)\s*(\d+)\s*:\s*(\d+))?\b`)
)

// parseValidatorRange accepts the common KMonad/Megaparsec line:column form
// plus explicit line/column ranges. It intentionally ignores unrecognised
// prose rather than guessing at a source location.
func parseValidatorRange(output string) (validatorRange, bool) {
	var result validatorRange
	found := false
	for _, pattern := range []*regexp.Regexp{validatorLongRange, validatorShortErrorRange, validatorShortStandaloneRange} {
		for _, match := range pattern.FindAllStringSubmatch(output, -1) {
			// Two parsed positions in one diagnostic are ambiguous even when
			// one happens to lie inside an assignment.
			if found {
				return validatorRange{}, false
			}
			startLine, startColumn, ok := parsePositivePosition(match[1], match[2])
			if !ok {
				return validatorRange{}, false
			}
			result = validatorRange{startLine: startLine, startColumn: startColumn}
			if match[4] != "" {
				endLineText := match[3]
				if endLineText == "" {
					endLineText = match[1]
				}
				endLine, endColumn, ok := parsePositivePosition(endLineText, match[4])
				if !ok {
					return validatorRange{}, false
				}
				result.endLine, result.endColumn = endLine, endColumn
			}
			found = true
		}
	}
	return result, found
}

func parsePositivePosition(line, column string) (int, int, bool) {
	parsedLine, lineErr := strconv.Atoi(line)
	parsedColumn, columnErr := strconv.Atoi(column)
	return parsedLine, parsedColumn, lineErr == nil && columnErr == nil && parsedLine > 0 && parsedColumn > 0
}

func lineColumnOffset(data []byte, line, column int) (int, bool) {
	if !utf8.Valid(data) || line < 1 || column < 1 {
		return 0, false
	}
	currentLine, currentColumn := 1, 1
	for offset := 0; offset < len(data); {
		if currentLine == line && currentColumn == column {
			return offset, true
		}
		runeValue, size := utf8.DecodeRune(data[offset:])
		offset += size
		if runeValue == '\n' {
			currentLine, currentColumn = currentLine+1, 1
		} else {
			currentColumn++
		}
	}
	return len(data), currentLine == line && currentColumn == column
}

func offsetLineColumn(data []byte, target int) (int, int) {
	line, column := 1, 1
	for offset := 0; offset < target; {
		runeValue, size := utf8.DecodeRune(data[offset:])
		offset += size
		if runeValue == '\n' {
			line, column = line+1, 1
		} else {
			column++
		}
	}
	return line, column
}

func nextRuneOffset(data []byte, offset int) (int, bool) {
	if offset < 0 || offset >= len(data) || !utf8.Valid(data) {
		return 0, false
	}
	_, size := utf8.DecodeRune(data[offset:])
	return offset + size, true
}

package data

import "golang.org/x/tools/go/packages"

type JSONResult struct {
	Issues []Issue
	Report *Data
}

type Issue struct {
	FromLinter string
	Text       string

	Severity string

	// Source lines of a code with the issue to show
	SourceLines []string

	// If we know how to fix the issue we can provide replacement lines
	Replacement *Replacement

	// Pkg is needed for proper caching of linting results
	Pkg *packages.Package `json:"-"`

	LineRange *Range `json:",omitempty"`

	Pos Position

	// HunkPos is used only when golangci-lint is run over a diff
	HunkPos int `json:",omitempty"`

	// If we are expecting a nolint (because this is from nolintlint), record the expected linter
	ExpectNoLint         bool
	ExpectedNoLintLinter string
}

type Range struct {
	From, To int
}

type Replacement struct {
	NeedOnlyDelete bool     // need to delete all lines of the issue without replacement with new lines
	NewLines       []string // is NeedDelete is false it's the replacement lines
	Inline         *InlineFix
}

type InlineFix struct {
	StartCol  int // zero-based
	Length    int // length of chunk to be replaced
	NewString string
}

type Position struct {
	Filename string // filename, if any
	Offset   int    // offset, starting at 0
	Line     int    // line number, starting at 1
	Column   int    // column number, starting at 1 (byte count)
}

type Warning struct {
	Tag  string `json:",omitempty"`
	Text string
}

type LinterData struct {
	Name             string
	Enabled          bool `json:",omitempty"`
	EnabledByDefault bool `json:",omitempty"`
}

type Data struct {
	Warnings []Warning    `json:",omitempty"`
	Linters  []LinterData `json:",omitempty"`
	Error    string       `json:",omitempty"`
}

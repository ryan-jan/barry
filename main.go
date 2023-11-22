package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/docopt/docopt-go"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

var usage = `Barry

Usage:
  barry [options] [<target>...]

Options:
  --no-list     Don't list files containing formatting inconsistencies.
  --no-write    Don't overwrite the input files. (This is implied by --check or when the input is STDIN.)
  --check       Check if the input is formatted. Exit status will be 0 if all input is properly formatted.
                If not, exit status will be non-zero and the command will output a list of filenames whose files are
				not properly formatted.
  --diff        Display diffs of formatting changes.
  -h --help     Show this screen.`

var config struct {
	NoList  bool     `docopt:"--no-list"`
	NoWrite bool     `docopt:"--no-write"`
	Check   bool     `docopt:"--check"`
	Diff    bool     `docopt:"--diff"`
	Target  []string `docopt:"<target>"`
}

func main() {
	args, _ := docopt.ParseDoc(usage)
	args.Bind(&config)

	var paths []string
	if len(config.Target) == 0 {
		paths = []string{"."}
	} else {
		paths = config.Target
	}

	if config.Check {
		config.NoList = false
		config.NoWrite = true
	}

	err := runFormat(paths)
	if err != nil {
		log.Fatal(err)
	}
}

func runFormat(paths []string) error {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("No file or directory at %s", path)
		}
		if info.IsDir() {
			// TODO: process dir
		} else {
			switch filepath.Ext(path) {
			case ".tf", ".tfvars":
				f, err := os.Open(path)
				if err != nil {
					// Open does not produce error messages that are end-user-appropriate, so we'll need to simplify
					// here.
					return fmt.Errorf("Failed to read file %s", path)
				}

				err = processFile(path, f)
				if err != nil {
					return err
				}
				f.Close()
			default:
				return fmt.Errorf("Only .tf and .tfvars files can be processed with terraform fmt")
			}
		}

	}
	return nil
}

func processFile(path string, r io.Reader) error {
	src, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("Failed to read %s", path)
	}
	// File must be parseable as HCL native syntax before we'll try to format
	// it. If not, the formatter is likely to make drastic changes that would
	// be hard for the user to undo.
	_, syntaxDiags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if syntaxDiags.HasErrors() {
		return fmt.Errorf("Failed to parse %s as HCL syntax", path)
	}

	result := formatSourceCode(src, path)
	result = formatFile(result)

	if !bytes.Equal(src, result) {
		// Something was changed.
		if !config.NoList {
			fmt.Println(path)
		}
		if !config.NoWrite {
			err := os.WriteFile(path, result, 0644)
			if err != nil {
				return fmt.Errorf("Failed to write %s", path)
			}
		}
		if config.Diff {
			diff, err := bytesDiff(src, result, path)
			if err != nil {
				return fmt.Errorf("Failed to generate diff for %s: %s", path, err)
			}
			os.Stdout.Write(diff)
		}
	}
	return nil
}

// formatFile is a pass which performs some general formatting without parsing the HCL itself.
func formatFile(src []byte) []byte {
	// Replace all occurrences of two or more blank lines with a single blank line.
	multipleBlankLines := regexp.MustCompile("(?m)(^\n{2,})")
	src = multipleBlankLines.ReplaceAll(src, []byte("\n"))

	// Ensure top-level blocks are separated by a single blank line. This also takes into account the fact that often
	// top-level blocks are preceded by a related comment.
	blocksMissingBlankLines := regexp.MustCompile(
		fmt.Sprintf("(?m)^}\n(%s|#|//|/\\*)", strings.Join(TopLevelBlocks, "|")))
	src = blocksMissingBlankLines.ReplaceAllFunc(src, func(m []byte) []byte {
		bracketNewline := regexp.MustCompile("^}\n")
		m = bracketNewline.ReplaceAll(m, []byte("}\n\n"))
		return m
	})

	// Replace all occurrences of the "//" comment token with the preferred "#" token.
	slashComments := regexp.MustCompile("/{2}\\s*")
	src = slashComments.ReplaceAll(src, []byte("# "))

	return src
}

// formatSourceCode is the formatting logic itself, applied to each file that is selected (directly or indirectly) on
// the command line.
func formatSourceCode(src []byte, filename string) []byte {
	f, diags := hclwrite.ParseConfig(src, filename, hcl.InitialPos)
	if diags.HasErrors() {
		// It would be weird to get here because the caller should already have
		// checked for syntax errors and returned them. We'll just do nothing
		// in this case, returning the input exactly as given.
		return src
	}

	formatBody(f.Body(), nil)

	return f.Bytes()
}

func formatBody(body *hclwrite.Body, inBlocks []string) {
	attrs := body.Attributes()
	blocks := body.Blocks()

	// Create a sorted list of attribute names so that we can ensure the order of these when we rewrite the file.
	attr_names := make([]string, 0, len(attrs))
	for name := range attrs {
		attr_names = append(attr_names, name)
	}
	sort.Strings(attr_names)

	// Sort the blocks array by block.Type() so that we can ensure the order of these when we rewrite the file.
	sort.Slice(blocks, func(a, b int) bool {
		return blocks[a].Type() < blocks[b].Type()
	})

	for name, attr := range attrs {
		if len(inBlocks) == 1 && inBlocks[0] == "variable" && name == "type" {
			cleanedExprTokens := formatTypeExpr(attr.Expr().BuildTokens(nil))
			body.SetAttributeRaw(name, cleanedExprTokens)
			continue
		}
		cleanedExprTokens := formatValueExpr(attr.Expr().BuildTokens(nil))
		body.SetAttributeRaw(name, cleanedExprTokens)
	}

	if len(inBlocks) > 0 {
		body.Clear()

		if isModuleBlock(inBlocks) {
			body.AppendNewline()
			for _, name := range attr_names {
				if !isModuleSrcVerAttribute(name) {
					continue
				}
				body.AppendUnstructuredTokens(attrs[name].BuildTokens(nil))
			}
		}
		if containsMetaAttributes(attrs) {
			body.AppendNewline()
			for _, name := range attr_names {
				if !isMetaAttribute(name) {
					continue
				}
				body.AppendUnstructuredTokens(attrs[name].BuildTokens(nil))
			}
		}
		if containsNonMetaAttributes(attrs) {
			body.AppendNewline()
			for _, name := range attr_names {
				if isMetaAttribute(name) || (isModuleBlock(inBlocks) && isModuleSrcVerAttribute(name)) {
					continue
				}
				body.AppendUnstructuredTokens(attrs[name].BuildTokens(nil))
			}
		}
		for i, block := range blocks {
			if isMetaAttribute(block.Type()) {
				continue
			}
			if i == 0 {
				body.AppendNewline()
			} else if block.Type() != blocks[i-1].Type() {
				body.AppendNewline()
			}
			body.AppendBlock(block)
		}
		for i, block := range blocks {
			if !isMetaAttribute(block.Type()) {
				continue
			}
			if i == 0 {
				body.AppendNewline()
			} else if block.Type() != blocks[i-1].Type() {
				body.AppendNewline()
			}
			body.AppendBlock(block)
		}
	}

	for _, block := range blocks {
		// Normalize the label formatting, removing any weird stuff like
		// interleaved inline comments and using the idiomatic quoted
		// label syntax.
		block.SetLabels(block.Labels())

		inBlocks := append(inBlocks, block.Type())
		formatBody(block.Body(), inBlocks)
	}
}

func formatValueExpr(tokens hclwrite.Tokens) hclwrite.Tokens {
	if len(tokens) < 5 {
		// Can't possibly be a "${ ... }" sequence without at least enough
		// tokens for the delimiters and one token inside them.
		return tokens
	}
	oQuote := tokens[0]
	oBrace := tokens[1]
	cBrace := tokens[len(tokens)-2]
	cQuote := tokens[len(tokens)-1]
	if oQuote.Type != hclsyntax.TokenOQuote || oBrace.Type != hclsyntax.TokenTemplateInterp || cBrace.Type != hclsyntax.TokenTemplateSeqEnd || cQuote.Type != hclsyntax.TokenCQuote {
		// Not an interpolation sequence at all, then.
		return tokens
	}

	inside := tokens[2 : len(tokens)-2]

	// We're only interested in sequences that are provable to be single
	// interpolation sequences, which we'll determine by hunting inside
	// the interior tokens for any other interpolation sequences. This is
	// likely to produce false negatives sometimes, but that's better than
	// false positives and we're mainly interested in catching the easy cases
	// here.
	quotes := 0
	for _, token := range inside {
		if token.Type == hclsyntax.TokenOQuote {
			quotes++
			continue
		}
		if token.Type == hclsyntax.TokenCQuote {
			quotes--
			continue
		}
		if quotes > 0 {
			// Interpolation sequences inside nested quotes are okay, because
			// they are part of a nested expression.
			// "${foo("${bar}")}"
			continue
		}
		if token.Type == hclsyntax.TokenTemplateInterp || token.Type == hclsyntax.TokenTemplateSeqEnd {
			// We've found another template delimiter within our interior
			// tokens, which suggests that we've found something like this:
			// "${foo}${bar}"
			// That isn't unwrappable, so we'll leave the whole expression alone.
			return tokens
		}
		if token.Type == hclsyntax.TokenQuotedLit {
			// If there's any literal characters in the outermost
			// quoted sequence then it is not unwrappable.
			return tokens
		}
	}

	// If we got down here without an early return then this looks like
	// an unwrappable sequence, but we'll trim any leading and trailing
	// newlines that might result in an invalid result if we were to
	// naively trim something like this:
	// "${
	//    foo
	// }"
	trimmed := trimNewlines(inside)

	// Finally, we check if the unwrapped expression is on multiple lines. If
	// so, we ensure that it is surrounded by parenthesis to make sure that it
	// parses correctly after unwrapping. This may be redundant in some cases,
	// but is required for at least multi-line ternary expressions.
	isMultiLine := false
	hasLeadingParen := false
	hasTrailingParen := false
	for i, token := range trimmed {
		switch {
		case i == 0 && token.Type == hclsyntax.TokenOParen:
			hasLeadingParen = true
		case token.Type == hclsyntax.TokenNewline:
			isMultiLine = true
		case i == len(trimmed)-1 && token.Type == hclsyntax.TokenCParen:
			hasTrailingParen = true
		}
	}
	if isMultiLine && !(hasLeadingParen && hasTrailingParen) {
		wrapped := make(hclwrite.Tokens, 0, len(trimmed)+2)
		wrapped = append(wrapped, &hclwrite.Token{
			Type:  hclsyntax.TokenOParen,
			Bytes: []byte("("),
		})
		wrapped = append(wrapped, trimmed...)
		wrapped = append(wrapped, &hclwrite.Token{
			Type:  hclsyntax.TokenCParen,
			Bytes: []byte(")"),
		})

		return wrapped
	}

	return trimmed
}

func formatTypeExpr(tokens hclwrite.Tokens) hclwrite.Tokens {
	switch len(tokens) {
	case 1:
		kwTok := tokens[0]
		if kwTok.Type != hclsyntax.TokenIdent {
			// Not a single type keyword, then.
			return tokens
		}

		// Collection types without an explicit element type mean
		// the element type is "any", so we'll normalize that.
		switch string(kwTok.Bytes) {
		case "list", "map", "set":
			return hclwrite.Tokens{
				kwTok,
				{
					Type:  hclsyntax.TokenOParen,
					Bytes: []byte("("),
				},
				{
					Type:  hclsyntax.TokenIdent,
					Bytes: []byte("any"),
				},
				{
					Type:  hclsyntax.TokenCParen,
					Bytes: []byte(")"),
				},
			}
		default:
			return tokens
		}

	case 3:
		// A pre-0.12 legacy quoted string type, like "string".
		oQuote := tokens[0]
		strTok := tokens[1]
		cQuote := tokens[2]
		if oQuote.Type != hclsyntax.TokenOQuote || strTok.Type != hclsyntax.TokenQuotedLit || cQuote.Type != hclsyntax.TokenCQuote {
			// Not a quoted string sequence, then.
			return tokens
		}

		// Because this quoted syntax is from Terraform 0.11 and
		// earlier, which didn't have the idea of "any" as an,
		// element type, we use string as the default element
		// type. That will avoid oddities if somehow the configuration
		// was relying on numeric values being auto-converted to
		// string, as 0.11 would do. This mimicks what terraform
		// 0.12upgrade used to do, because we'd found real-world
		// modules that were depending on the auto-stringing.)
		switch string(strTok.Bytes) {
		case "string":
			return hclwrite.Tokens{
				{
					Type:  hclsyntax.TokenIdent,
					Bytes: []byte("string"),
				},
			}
		case "list":
			return hclwrite.Tokens{
				{
					Type:  hclsyntax.TokenIdent,
					Bytes: []byte("list"),
				},
				{
					Type:  hclsyntax.TokenOParen,
					Bytes: []byte("("),
				},
				{
					Type:  hclsyntax.TokenIdent,
					Bytes: []byte("string"),
				},
				{
					Type:  hclsyntax.TokenCParen,
					Bytes: []byte(")"),
				},
			}
		case "map":
			return hclwrite.Tokens{
				{
					Type:  hclsyntax.TokenIdent,
					Bytes: []byte("map"),
				},
				{
					Type:  hclsyntax.TokenOParen,
					Bytes: []byte("("),
				},
				{
					Type:  hclsyntax.TokenIdent,
					Bytes: []byte("string"),
				},
				{
					Type:  hclsyntax.TokenCParen,
					Bytes: []byte(")"),
				},
			}
		default:
			// Something else we're not expecting, then.
			return tokens
		}
	default:
		return tokens
	}
}

func trimNewlines(tokens hclwrite.Tokens) hclwrite.Tokens {
	if len(tokens) == 0 {
		return nil
	}
	var start, end int
	for start = 0; start < len(tokens); start++ {
		if tokens[start].Type != hclsyntax.TokenNewline {
			break
		}
	}
	for end = len(tokens); end > 0; end-- {
		if tokens[end-1].Type != hclsyntax.TokenNewline {
			break
		}
	}
	return tokens[start:end]
}

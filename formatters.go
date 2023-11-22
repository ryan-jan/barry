package main

import (
	"regexp"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// formatSourceCode is the formatting logic itself, applied to each file that is selected (directly or indirectly) on
// the command line.
func formatSourceCode(src []byte, filename string) []byte {
	tokens, _ := hclsyntax.LexConfig(src, filename, hcl.Pos{Line: 1, Column: 1})
	src = formatLexTokens(tokens)

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

// formatLexTokens performs some general formatting using raw tokens from the LexConfig function.
func formatLexTokens(tokens hclsyntax.Tokens) []byte {
	var out []byte
	var resume int = 0
	for i, token := range tokens {
		if i < resume {
			continue
		}

		tokenType := token.Type
		tokenStartCol := token.Range.Start.Column
		isEmptyLine := tokenType == hclsyntax.TokenNewline && tokenStartCol == 1

		if tokenType == hclsyntax.TokenComment {
			// Replace all occurrences of the "//" comment token with the preferred "#" token.
			slashComments := regexp.MustCompile("^\\/\\/")
			token.Bytes = slashComments.ReplaceAll(token.Bytes, []byte("#"))
		}

		var prevTokenType hclsyntax.TokenType
		if i > 0 {
			prevTokenType = tokens[i-1].Type
			prevStartCol := tokens[i-1].Range.Start.Column
			prevIsEmptyLine := tokens[i-1].Type == hclsyntax.TokenNewline && prevStartCol == 1
			if isEmptyLine && prevIsEmptyLine {
				// Remove duplicate empty line by not appending it to outSrc.
				continue
			}
		}

		out = append(out, token.Bytes...)

		// This ensures top-level blocks are separated by a single empty line.
		if i < len(tokens)-3 {
			nextStartCol := tokens[i+2].Range.Start.Column
			nextIsEmptyLine := tokens[i+2].Type == hclsyntax.TokenNewline && nextStartCol == 1
			if tokenType == hclsyntax.TokenCBrace && tokenStartCol == 1 {
				if !nextIsEmptyLine {
					out = append(out, byte(hclsyntax.TokenNewline))
				}
			}
		}

		// This ensures that there are no "dangling" comments in blocks. A dangling comment is one which is not directly
		// attached to a block or attribute. For example, the following is a dangling comment.
		//
		// # I am dangling.
		//
		// # I am also dangling.
		//
		// resource "some_resource" "example" {
		// ...
		//
		// A dangling comment would be destroyed as part of our re-ordering of attributes/blocks later on. As such, we
		// ensure that the comments are "attached" to the block/attribute immediately following, and that any empty
		// lines within the comment are prefixed with a single "#" character. So the above example becomes:
		//
		// # I am dangling.
		// #
		// # I am not dangling.
		// #
		// resource "some_resource" "example" {
		// ...
		//
		if tokenType == hclsyntax.TokenComment && tokenStartCol > 1 && prevTokenType == hclsyntax.TokenNewline {
			ii := i
			ii++
			nextToken := tokens[ii]
			for nextToken.Type == hclsyntax.TokenComment || nextToken.Type == hclsyntax.TokenNewline {
				if nextToken.Type == hclsyntax.TokenNewline {
					nextToken.Bytes = []byte("#\n")
				}
				out = append(out, nextToken.Bytes...)
				ii++
				nextToken = tokens[ii]
			}
			resume = ii
		}
	}
	return out
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

	// This ensures the order of the top-level blocks in the file is left unchanged. We only re-order the arguments and
	// nested blocks _within_ each top-level block.
	if len(inBlocks) > 0 {
		body.Clear()

		if isModuleBlock(inBlocks) {
			body.AppendNewline()
			for i, name := range attr_names {
				if isModuleSrcVerAttribute(name) {
					appendAttribute(body, attrs[name], i)
				}
			}
		}
		if containsMetaAttributes(attrs) {
			body.AppendNewline()
			for i, name := range attr_names {
				if isMetaAttribute(name) {
					appendAttribute(body, attrs[name], i)
				}
			}
		}
		if containsNonMetaAttributes(attrs) {
			body.AppendNewline()
			for i, name := range attr_names {
				if !isMetaAttribute(name) && !(isModuleBlock(inBlocks) && isModuleSrcVerAttribute(name)) {
					appendAttribute(body, attrs[name], i)
				}
			}
		}
		for i, block := range blocks {
			if !isMetaAttribute(block.Type()) {
				appendBlock(body, block, blocks, i)
			}
		}
		for i, block := range blocks {
			if isMetaAttribute(block.Type()) {
				appendBlock(body, block, blocks, i)
			}
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

package main

import (
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

var MetaArgumentNames = []string{
	"count",
	"depends_on",
	"for_each",
	"lifecycle",
	"provider",
	"providers",
}
var TopLevelBlocks = []string{
	"data",
	"locals",
	"module",
	"output",
	"provider",
	"resource",
	"terraform",
	"variable",
}
var fmtSupportedExts = []string{
	".tf",
	".tfvars",
	".tftest.hcl",
}

func containsMetaAttributes(attrs map[string]*hclwrite.Attribute) bool {
	for name := range attrs {
		if isMetaAttribute(name) {
			return true
		}
	}
	return false
}

func containsNonMetaAttributes(attrs map[string]*hclwrite.Attribute) bool {
	for name := range attrs {
		if !isMetaAttribute(name) {
			return true
		}
	}
	return false
}

func isMetaAttribute(name string) bool {
	if slices.Contains(MetaArgumentNames, name) {
		return true
	} else {
		return false
	}
}

func isModuleBlock(inBlocks []string) bool {
	if len(inBlocks) == 1 && inBlocks[0] == "module" {
		return true
	} else {
		return false
	}
}

func isModuleSrcVerAttribute(name string) bool {
	if slices.Contains([]string{"source", "version"}, name) {
		return true
	} else {
		return false
	}
}

func bytesDiff(b1, b2 []byte, path string) (data []byte, err error) {
	f1, err := os.CreateTemp("", "")
	if err != nil {
		return
	}
	defer os.Remove(f1.Name())
	defer f1.Close()

	f2, err := os.CreateTemp("", "")
	if err != nil {
		return
	}
	defer os.Remove(f2.Name())
	defer f2.Close()

	f1.Write(b1)
	f2.Write(b2)

	data, err = exec.Command("diff", "--label=old/"+path, "--label=new/"+path, "-u", f1.Name(), f2.Name()).CombinedOutput()
	if len(data) > 0 {
		// diff exits with a non-zero status when the files don't match.
		// Ignore that failure as long as we get output.
		err = nil
	}
	return
}

// IsIgnoredFile returns true if the given filename (which must not have a
// directory path ahead of it) should be ignored as e.g. an editor swap file.
func IsIgnoredFile(name string) bool {
	return strings.HasPrefix(name, ".") || // Unix-like hidden files
		strings.HasSuffix(name, "~") || // vim
		strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#") // emacs
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

func appendAttribute(body *hclwrite.Body, attr *hclwrite.Attribute, index int) {
	tokens := attr.BuildTokens(nil)

	// Separate comments from previous attributes with an empty line.
	if index > 0 && tokens[0].Type == hclsyntax.TokenComment {
		body.AppendNewline()
	}
	body.AppendUnstructuredTokens(tokens)
}

func appendBlock(body *hclwrite.Body, block *hclwrite.Block, blocks []*hclwrite.Block, index int) {
	if index == 0 {
		body.AppendNewline()
	} else if block.Type() != blocks[index-1].Type() {
		body.AppendNewline()
	}
	body.AppendBlock(block)
}

package main

import (
	"os"
	"os/exec"
	"slices"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

var MetaArgumentNames = []string{"count", "depends_on", "for_each", "lifecycle", "provider", "providers"}
var TopLevelBlocks = []string{"data", "locals", "module", "output", "provider", "resource", "terraform", "variable"}

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

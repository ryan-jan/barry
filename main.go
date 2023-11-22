package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docopt/docopt-go"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
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
  --recursive
  -h --help     Show this screen.`

var config struct {
	NoList    bool     `docopt:"--no-list"`
	NoWrite   bool     `docopt:"--no-write"`
	Check     bool     `docopt:"--check"`
	Diff      bool     `docopt:"--diff"`
	Recursive bool     `docopt:"--recursive"`
	Target    []string `docopt:"<target>"`
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
			err := processDir(path)
			if err != nil {
				return err
			}
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

func processDir(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		switch {
		case os.IsNotExist(err):
			return fmt.Errorf("There is no configuration directory at %s", path)
		default:
			// ReadDir does not produce error messages that are end-user-appropriate,
			// so we'll need to simplify here.
			return fmt.Errorf("Cannot read directory %s", path)
		}
	}

	for _, info := range entries {
		name := info.Name()
		if IsIgnoredFile(name) {
			continue
		}
		subPath := filepath.Join(path, name)
		if info.IsDir() {
			if config.Recursive {
				err := processDir(subPath)
				if err != nil {
					return err
				}
			}

			// We do not recurse into child directories by default because we
			// want to mimic the file-reading behaviour of "terraform plan", etc,
			// operating on one module at a time.
			continue
		}

		for _, ext := range fmtSupportedExts {
			if strings.HasSuffix(name, ext) {
				f, err := os.Open(subPath)
				if err != nil {
					// Open does not produce error messages that are end-user-appropriate,
					// so we'll need to simplify here.
					return fmt.Errorf("Failed to read file %s", subPath)
				}

				err = processFile(subPath, f)
				f.Close()
				if err != nil {
					return err
				}

				// Don't need to check the remaining extensions.
				break
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

	trailingWhitespace := regexp.MustCompile("(?m)[ \\t]+$")
	src = trailingWhitespace.ReplaceAll(src, []byte(""))

	// File must be parseable as HCL native syntax before we'll try to format
	// it. If not, the formatter is likely to make drastic changes that would
	// be hard for the user to undo.
	_, syntaxDiags := hclsyntax.ParseConfig(src, path, hcl.Pos{Line: 1, Column: 1})
	if syntaxDiags.HasErrors() {
		return fmt.Errorf("Failed to parse %s as HCL syntax", path)
	}

	result := formatSourceCode(src, path)

	if !bytes.Equal(src, result) {
		// Something changed.
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

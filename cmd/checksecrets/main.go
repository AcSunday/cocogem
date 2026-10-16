// Command checksecrets rejects common credential material in tracked text files.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

var secrets = []*regexp.Regexp{
	regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`),
}

func containsSecret(b []byte) bool {
	for _, pattern := range secrets {
		if pattern.Match(b) {
			return true
		}
	}
	return false
}
func main() {
	files, err := exec.Command("git", "ls-files", "-z").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	failed := false
	for _, path := range bytes.Split(files, []byte{0}) {
		if len(path) == 0 {
			continue
		}
		b, err := os.ReadFile(string(path))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			failed = true
			continue
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		if containsSecret(b) {
			fmt.Fprintf(os.Stderr, "Potential credential material in %s\n", path)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

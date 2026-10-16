package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/k2so-dev/awgx/internal/validate"
)

type exitError struct {
	code int
}

func (e exitError) Error() string {
	return fmt.Sprintf("exit %d", e.code)
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

var input = bufio.NewReader(os.Stdin)

func confirm(yes bool, question string) bool {
	if yes {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	line, _ := input.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func printIssues(w io.Writer, is validate.Issues) {
	for _, i := range is {
		fmt.Fprintln(w, i.String())
	}
}

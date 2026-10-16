package main

import (
	"os"

	"github.com/k2so-dev/awgx/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}

package main

import (
	"os"

	"github.com/miradorlabs/mirador-cli/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}

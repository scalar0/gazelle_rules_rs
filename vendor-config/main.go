package main

import (
	"flag"
	"fmt"
	"os"

	rulesrs "github.com/scalar0/gazelle_rules_rs"
)

func main() {
	root := flag.String("root", ".", "Repository root directory.")
	check := flag.Bool("check", false, "Check the Cargo vendor block without changes.")
	write := flag.Bool("write", false, "Update the Cargo vendor block.")
	flag.Parse()
	if (*check && *write) || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Use either --check or --write without positional arguments.")
		os.Exit(2)
	}
	if err := rulesrs.SyncVendorConfig(*root, *write); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

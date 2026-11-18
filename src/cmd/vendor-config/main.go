package main

import (
	"flag"
	"fmt"
	"os"

	rulesrs "github.com/scalar0/gazelle_rules_rs/src"
)

func main() {
	root := flag.String("root", ".", "Repository root directory.")
	check := flag.Bool("check", false, "Check the Cargo vendor block without changes.")
	write := flag.Bool("write", false, "Update the Cargo vendor block.")
	printInputs := flag.Bool("print-build-inputs", false, "Print the resolved workspace build inputs as JSON.")
	flag.Parse()
	if (*check && *write) || (*printInputs && (*check || *write)) || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Select one mode without positional arguments.")
		os.Exit(2)
	}
	var err error
	if *printInputs {
		err = rulesrs.PrintBuildInputs(*root, os.Stdout)
	} else {
		err = rulesrs.SyncVendorConfig(*root, *write)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

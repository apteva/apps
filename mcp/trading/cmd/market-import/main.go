// market-import converts supplied CSV exports to a portable sourced event tape.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/apteva/apps/mcp/trading/internal/marketdata"
)

func run() error {
	spec := flag.String("spec", "", "JSON import specification with symbol and streams (CSV inline)")
	out := flag.String("output", "", "normalized dataset JSON path")
	flag.Parse()
	if *spec == "" || *out == "" {
		return errors.New("required: --spec --output")
	}
	f, err := os.Open(*spec)
	if err != nil {
		return err
	}
	defer f.Close()
	var req marketdata.Request
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return err
	}
	dataset, err := marketdata.Import(req)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(dataset)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(raw, '\n'), 0644)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

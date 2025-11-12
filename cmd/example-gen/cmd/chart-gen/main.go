package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/elastic/crd-ref-docs/config"
	"github.com/elastic/crd-ref-docs/processor"
	"github.com/pkg/errors"

	"github.com/percona/percona-server-mysql-operator/cmd/example-gen/internal/render"
)

//go:embed templates/*.tpl
var templates embed.FS

func main() {
	if len(os.Args) != 2 {
		fmt.Printf("Expected 1 argument (source path). Got %d\n", len(os.Args)-1)
		os.Exit(1)
	}
	if err := printHelm(os.Args[1]); err != nil {
		panic(err)
	}
}

func printHelm(sourcePath string) error {
	conf := &config.Config{
		Processor: config.ProcessorConfig{
			IgnoreTypes: []string{
				"PerconaServerMySQLStatus",
				"PiTRSpec",
			},
			IgnoreFields: []string{
				// Deprecated field
				"initImage",
			},
		},
		Flags: config.Flags{
			SourcePath: sourcePath,
			MaxDepth:   20,
		},
	}

	gvd, err := processor.Process(conf)
	if err != nil {
		return errors.Wrap(err, "process")
	}
	if len(gvd) != 1 {
		return errors.New("unexpected gvd length")
	}

	t, ok := gvd[0].Types["PerconaServerMySQL"]
	if !ok {
		return errors.New("PerconaServerMySQL type is not found")
	}
	if err := render.Helm(os.Stdout, t, templates); err != nil {
		return errors.Wrap(err, "render")
	}

	return nil
}

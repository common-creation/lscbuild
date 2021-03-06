package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/common-creation/lscbuild/lib"
	"github.com/urfave/cli/v2"
)

const DEFAULT_YAML_PATH = "./.lscbuild.yaml"

var (
	Version string
)

func main() {
	cli.VersionFlag = &cli.BoolFlag{
		Name:  "version",
		Usage: "show version",
	}
	app := &cli.App{
		Name:      "lscbuild",
		Usage:     "Lightweight, Simple, Configuable builder",
		Version:   Version,
		Copyright: "© 2022 Common Creation, LLC",
	}
	app.Commands = []*cli.Command{
		{
			Name:  "run",
			Usage: "run all or specific jobs",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "yaml",
					Usage:       "path of '.lscbuild.yaml'",
					DefaultText: "./.lscbuild.yaml",
				},
				&cli.StringFlag{
					Name:  "job",
					Usage: "comma-separated target job names (run all if not present)",
				},
			},
			Action: run,
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

func run(c *cli.Context) error {
	yamlPath := c.String("yaml")
	if yamlPath == "" {
		yamlPath = DEFAULT_YAML_PATH
	}
	parser := lib.NewParser(&lib.ParserConfig{
		YamlPath: yamlPath,
	})
	executor, err := parser.Parse()
	if err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("[lscbuild] yaml parse error: %w", err))
		os.Exit(1)
	}

	jobs := c.String("job")
	jobSlice := strings.Split(jobs, ",")
	if jobs == "" {
		jobSlice = nil
	}
	if err := executor.Run(jobSlice...); err != nil {
		fmt.Fprintln(os.Stderr, fmt.Errorf("[lscbuild] job execute error: %w", err))
		os.Exit(1)
	}

	return nil
}

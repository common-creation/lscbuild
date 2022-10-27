package lib

import (
	"errors"
	"os"
	"strconv"

	v1 "github.com/common-creation/lscbuild/lib/v1"
	"github.com/common-creation/lscbuild/typing"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

type (
	ParserConfig struct {
		YamlPath  string
		GlobalEnv []string
	}
	Parser struct {
		parserConfig *ParserConfig
	}
)

func NewParser(config *ParserConfig) *Parser {
	return &Parser{
		parserConfig: config,
	}
}

func (p *Parser) Parse() (typing.Executor, error) {
	b, err := os.ReadFile(p.parserConfig.YamlPath)
	if err != nil {
		return nil, err
	}

	lscbuild := new(typing.LSCBuild)
	if err := yaml.Unmarshal(b, lscbuild); err != nil {
		return nil, err
	}
	if lscbuild.Version == nil {
		lscbuild.Version = lo.ToPtr(1)
	}

	switch *lscbuild.Version {
	case 1:
		executorV1, err := v1.NewV1Executer(b)
		if err != nil {
			return nil, err
		}
		if p.parserConfig.GlobalEnv != nil && len(p.parserConfig.GlobalEnv) > 0 {
			executorV1.SetGlobalEnv(p.parserConfig.GlobalEnv)
		}
		if err := yaml.Unmarshal(b, lscbuild); err != nil {
			return nil, err
		}
		return executorV1, nil
	}

	return nil, errors.New("unsupported yaml version: " + strconv.Itoa(*lscbuild.Version))
}

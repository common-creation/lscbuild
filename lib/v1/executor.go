package v1

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/common-creation/lscbuild/typing"
	"github.com/goccy/go-yaml"
	"github.com/mitchellh/mapstructure"
	"github.com/samber/lo"
)

type (
	Executor struct {
		typing.Executor
		lscBuild *LSCBuild
	}
	LSCBuild struct {
		typing.LSCBuild
		Jobs yaml.MapSlice `yaml:"jobs"`
	}
	Job struct {
		Steps []Step   `yaml:"steps"`
		Shell string   `yaml:"shell"`
		Env   []string `yaml:"env"`
	}
	Step struct {
		Cmd string   `yaml:"cmd"`
		Dir string   `yaml:"dir"`
		Env []string `yaml:"env"`
	}
)

func NewV1Executer(body []byte) (*Executor, error) {
	lscBuild := new(LSCBuild)
	if err := yaml.Unmarshal(body, lscBuild); err != nil {
		return nil, err
	}
	return &Executor{
		lscBuild: lscBuild,
	}, nil
}

func (e *Executor) Run(jobs ...string) error {
	targets := make([]string, 0)
	yamlJobNames := make([]string, 0)

	for key := range e.lscBuild.Jobs.ToMap() {
		yamlJobNames = append(yamlJobNames, key.(string))
	}

	if len(jobs) == 0 {
		targets = yamlJobNames
	} else {
		for _, job := range jobs {
			if _, ok := lo.Find(yamlJobNames, func(s string) bool { return s == job }); !ok {
				return errors.New("job '" + job + "' is not found")
			} else {
				targets = append(targets, job)
			}
		}
	}

	for _, job := range e.lscBuild.Jobs {
		if _, ok := lo.Find(targets, func(s string) bool { return s == job.Key }); ok {
			decoded := new(Job)
			if err := mapstructure.Decode(job.Value, decoded); err != nil {
				return err
			}
			if err := e.runJob(job.Key.(string), decoded); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *Executor) runJob(name string, job *Job) (result error) {
	fmt.Fprintf(os.Stderr, "[lscbuild] start job: %s\n", name)
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case string:
				result = errors.New(e)
			case error:
				result = e
			default:
				result = errors.New("unknown job error")
			}
		}
	}()

	if job.Shell == "" {
		job.Shell = "/bin/sh"
	}

	shellArgs := strings.Split(job.Shell, " ")

	for _, step := range job.Steps {
		cmd := exec.Command(shellArgs[0], append(shellArgs[1:], "-c", step.Cmd)...)
		if step.Dir != "" {
			if absolutePath, err := filepath.Abs(step.Dir); err == nil {
				cmd.Dir = absolutePath
			}
		}

		env := os.Environ()
		if len(job.Env) > 0 {
			cmd.Env = append(env, job.Env...)
		} else {
			cmd.Env = env
		}
		if len(step.Env) > 0 {
			cmd.Env = append(cmd.Env, step.Env...)
		}
		cmd.Env = append(cmd.Env, "SHELL="+job.Shell)

		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		fmt.Fprintf(os.Stderr, "[lscbuild] start command: %s\n", step.Cmd)

		if err := cmd.Start(); err != nil {
			result = err
			return result
		}
		if err := cmd.Wait(); err != nil {
			result = err
			return result
		}
	}

	return result
}

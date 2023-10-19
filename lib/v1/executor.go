package v1

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/common-creation/lscbuild/typing"
	"github.com/common-creation/lscbuild/util"
	"github.com/goccy/go-yaml"
	"github.com/mitchellh/mapstructure"
	"github.com/samber/lo"
)

type (
	Executor struct {
		typing.Executor
		lscBuild  *LSCBuild
		globalEnv []string
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
		Name *string  `yaml:"name"`
		Cmd  string   `yaml:"cmd"`
		Dir  string   `yaml:"dir"`
		Env  []string `yaml:"env"`
		If   []If     `yaml:"if"`
	}
	If struct {
		Directory *IfExists `yaml:"directory,omitempty"`
		File      *IfExists `yaml:"file,omitempty"`
		Env       *[]string `yarl:"env,omitempty"`
	}
	IfExists struct {
		Exists  *string `yaml:"exists,omitempty"`
		Missing *string `yaml:"missing,omitempty"`
	}
)

func NewV1Executer(body []byte) (*Executor, error) {
	lscBuild := new(LSCBuild)
	if err := yaml.Unmarshal(body, lscBuild); err != nil {
		return nil, err
	}
	return &Executor{
		lscBuild:  lscBuild,
		globalEnv: make([]string, 0),
	}, nil
}

func (e *Executor) SetGlobalEnv(env []string) {
	e.globalEnv = env
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

func (e *Executor) stepName(step *Step) string {
	if step.Name != nil && *step.Name != "" {
		return *step.Name
	}
	return step.Cmd
}

func (e *Executor) prepareStep(name string, step *Step) bool {
	util.LogDebug("[lscbuild] prepare step: %s\n", name)

	if len(step.If) == 0 {
		return true
	}

	for _, i := range step.If {
		if i.File != nil {
			if i.File.Exists != nil {
				absolutePath, err := filepath.Abs(*i.File.Exists)
				if err != nil {
					return false
				}
				if _, err := os.Stat(absolutePath); err != nil {
					return false
				}
			}
			if i.File.Missing != nil {
				absolutePath, err := filepath.Abs(*i.File.Missing)
				if err != nil {
					return false
				}
				if _, err := os.Stat(absolutePath); err == nil {
					return false
				}
			}
		}
		if i.Directory != nil {
			if i.Directory.Exists != nil {
				absolutePath, err := filepath.Abs(*i.Directory.Exists)
				if err != nil {
					return false
				}
				if _, err := os.Stat(absolutePath); err != nil {
					return false
				}
			}
			if i.Directory.Missing != nil {
				absolutePath, err := filepath.Abs(*i.Directory.Missing)
				if err != nil {
					return false
				}
				if _, err := os.Stat(absolutePath); err == nil {
					return false
				}
			}
		}
		if i.Env != nil {
			for _, e1 := range *i.Env {
				if _, ok := lo.Find(step.Env, func(e2 string) bool { return e1 == e2 }); !ok {
					return false
				}
			}
		}
	}

	return true
}

func (e *Executor) runJob(name string, job *Job) (result error) {
	util.LogInfo("[lscbuild] start job: %s\n", name)
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

	isWindows := util.IsWindows()

	if job.Shell == "" {
		if isWindows {
			job.Shell = "cmd.exe /c"
		} else {
			job.Shell = "/bin/sh"
		}
	}

	shellArgs := strings.Split(job.Shell, " ")

	for _, step := range job.Steps {
		stepName := e.stepName(&step)

		if isWindows {
			if strings.Contains(job.Shell, "cmd") {
				file, err := os.CreateTemp(os.TempDir(), "*.bat")
				if err != nil {
					panic(err)
				}
				defer func() {
					file.Close()
					os.Remove(file.Name())
				}()

				file.WriteString("@echo off\n" + step.Cmd)
				file.Sync()
				file.Close()
				step.Cmd = file.Name()
			}
			if strings.Contains(job.Shell, "powershell") {
				file, err := os.CreateTemp(os.TempDir(), "*.ps1")
				if err != nil {
					panic(err)
				}
				defer func() {
					file.Close()
					os.Remove(file.Name())
				}()

				file.WriteString(step.Cmd)
				file.Sync()
				file.Close()
				step.Cmd = file.Name()
			}
		}

		cmd := exec.Command(shellArgs[0], append(shellArgs[1:], step.Cmd)...)
		if step.Dir != "" {
			if absolutePath, err := filepath.Abs(step.Dir); err == nil {
				cmd.Dir = absolutePath
			}
		}

		env := os.Environ()
		env = append(env, "LSCBUILD=1")

		if len(job.Env) > 0 {
			env = append(env, job.Env...)
		}
		if len(step.Env) > 0 {
			env = append(env, step.Env...)
		}
		env = append(env, e.globalEnv...)
		env = append(env, "SHELL="+job.Shell)

		cmd.Env = env
		step.Env = env

		if !e.prepareStep(stepName, &step) {
			util.LogInfo("[lscbuild] skip step: %s\n", stepName)
			continue
		}

		cmd.Stdin = nil
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		util.LogInfo("[lscbuild] start step: %s\n", stepName)

		if err := cmd.Start(); err != nil {
			result = err
			return result
		}
		if err := cmd.Wait(); err != nil {
			result = err
			return result
		}

		util.LogInfo("[lscbuild] end step: %s\n", stepName)
	}

	return result
}

package v1

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/common-creation/lscbuild/typing"
	"github.com/common-creation/lscbuild/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/goccy/go-yaml"
	"github.com/mitchellh/mapstructure"
	"github.com/samber/lo"
)

type (
	Executor struct {
		typing.Executor
		lscBuild    *LSCBuild
		globalEnv   []string
		pluginCache map[string]*Plugin
	}
	LSCBuild struct {
		typing.LSCBuild
		Jobs   yaml.MapSlice `yaml:"jobs"`
		Plugin Plugin        `yaml:"plugin"`
	}
	Job struct {
		Steps []Step   `yaml:"steps"`
		Shell string   `yaml:"shell"`
		Env   []string `yaml:"env"`
	}
	Plugin struct {
		Job
		LifeCycle PluginLifeCycle `yaml:"lifecycle"`
		repoPath  string
	}
	PluginLifeCycle struct {
		Initialize        *Step `yaml:"initialize,omitempty"`
		InitializeWindows *Step `yaml:"initialize_windows,omitempty"`
		Run               *Step `yaml:"run,omitempty"`
		RunWindows        *Step `yaml:"run_windows,omitempty"`
		Finalize          *Step `yaml:"finalize,omitempty"`
		FinalizeWindows   *Step `yaml:"finalize_windows,omitempty"`
	}
	Step struct {
		Name *string  `yaml:"name"`
		Use  string   `yaml:"use"`
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
	GitRepoInfo struct {
		GitRepo         string
		ReferencePrefix string
		ReferenceTarget string
	}
)

func NewV1Executer(body []byte) (*Executor, error) {
	lscBuild := new(LSCBuild)
	if err := yaml.Unmarshal(body, lscBuild); err != nil {
		return nil, err
	}
	return &Executor{
		lscBuild:    lscBuild,
		globalEnv:   make([]string, 0),
		pluginCache: make(map[string]*Plugin),
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

			util.LogInfo("[lscbuild] initialize plugins\n")
			if err := e.initializePlugins(job.Key.(string), decoded); err != nil {
				return err
			}

			util.LogInfo("[lscbuild] execute jobs\n")
			if err := e.runJob(job.Key.(string), decoded); err != nil {
				return err
			}

			util.LogInfo("[lscbuild] finalize plugins\n")
			if err := e.finalizePlugins(); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *Executor) stepName(step *Step) string {
	var name string
	if step.Name != nil && *step.Name != "" {
		name = *step.Name
	} else if step.Use != "" {
		name = step.Use
	} else {
		name = step.Cmd
	}

	name = strings.ReplaceAll(name, "\n", "; ")
	if len(name) > 50 {
		name = name[:50] + "..."
	}
	return name
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

func (e *Executor) parsePluginUse(use string) GitRepoInfo {
	var gitRepo string
	var referencePrefix string
	var referenceTarget string

	if strings.Contains(use, "@") {
		gitArgs := strings.Split(use, "@")
		gitRepo = gitArgs[0]
		referencePrefix = "refs/tags/"
		referenceTarget = gitArgs[1]
	} else if strings.Contains(use, "#") {
		gitArgs := strings.Split(use, "#")
		gitRepo = gitArgs[0]
		referencePrefix = "refs/heads/"
		referenceTarget = gitArgs[1]
	} else {
		gitRepo = use
	}

	return GitRepoInfo{
		GitRepo:         gitRepo,
		ReferencePrefix: referencePrefix,
		ReferenceTarget: referenceTarget,
	}
}

func (e *Executor) getRepoPath(gitRepo string) string {
	repoPath := filepath.Join(os.TempDir(), "lscbuild-plugins")
	lo.ForEach[string](strings.Split(gitRepo, "/"), func(s string, i int) {
		repoPath = filepath.Join(repoPath, s)
	})
	return repoPath
}

func (e *Executor) initializePlugins(name string, job *Job) (result error) {
	util.LogInfo("[lscbuild] check plugins: %s\n", name)
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case string:
				result = errors.New(e)
			case error:
				result = e
			default:
				result = errors.New("unknown plugin error")
			}
		}
	}()

	// NOTE: prepare plugins
	tmpBaseDir := filepath.Join(os.TempDir(), "lscbuild-plugins")
	os.MkdirAll(tmpBaseDir, os.ModePerm)

	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	for _, step := range job.Steps {
		if step.Use != "" {
			stepName := e.stepName(&step)
			util.LogInfo("[lscbuild] fetch plugin: %s\n", stepName)

			parsed := e.parsePluginUse(step.Use)
			repoPath := e.getRepoPath(parsed.GitRepo)
			if s, err := os.Stat(repoPath); err == nil && s.IsDir() {
				panic("plugin '" + parsed.GitRepo + "' is already exists")
			}

			_, err := git.PlainClone(repoPath, false, &git.CloneOptions{
				URL:           "https://github.com/" + parsed.GitRepo + ".git",
				ReferenceName: plumbing.ReferenceName(parsed.ReferencePrefix + parsed.ReferenceTarget),
				Progress:      os.Stdout,
			})
			if err != nil {
				panic(err)
			}

			pluginYamlPath := filepath.Join(repoPath, ".lscbuild.yaml")
			b, err := os.ReadFile(pluginYamlPath)
			if err != nil {
				result = err
				return result
			}
			lscbuild := new(LSCBuild)
			if err := yaml.Unmarshal(b, lscbuild); err != nil {
				result = err
				return result
			}
			e.pluginCache[parsed.GitRepo] = &lscbuild.Plugin
			e.pluginCache[parsed.GitRepo].repoPath = repoPath

			if e.pluginCache[parsed.GitRepo].LifeCycle.Initialize != nil {
				initializeStep := e.pluginCache[parsed.GitRepo].LifeCycle.Initialize.MustCopy()
				initializeStep.Dir = repoPath
				initializeStep.Env = append(initializeStep.Env, "LSCBUILD_CWD="+cwd)

				if err := e.runStep(&e.pluginCache[parsed.GitRepo].Job, &initializeStep); err != nil {
					result = err
					return result
				}
			}
		}
	}

	return result
}

func (e *Executor) finalizePlugins() (result error) {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	for _, v := range e.pluginCache {
		if v.LifeCycle.Finalize != nil {
			finalizeStep := v.LifeCycle.Finalize.MustCopy()
			finalizeStep.Dir = v.repoPath
			finalizeStep.Env = append(finalizeStep.Env, "LSCBUILD_CWD="+cwd)

			defer func() {
				os.RemoveAll(v.repoPath)
			}()
			if err := e.runStep(&v.Job, &finalizeStep); err != nil {
				result = err
				return result
			}
		}
	}

	return result
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
			job.Shell = "/bin/sh -e"
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	for _, step := range job.Steps {
		if step.Use != "" {
			parsed := e.parsePluginUse(step.Use)
			cache := e.pluginCache[parsed.GitRepo]

			if cache.LifeCycle.Run != nil {
				newStep := cache.LifeCycle.Run.MustCopy()
				for _, v := range step.Env {
					newStep.Env = append(newStep.Env, v)
				}
				if newStep.Dir == "" {
					newStep.Dir = cache.repoPath
				}
				newStep.Env = append(newStep.Env, "LSCBUILD_CWD="+cwd)

				result = e.runStep(job, &newStep)
				if result != nil {
					return result
				}
			}
		} else {
			result = e.runStep(job, &step)
			if result != nil {
				return result
			}
		}
	}

	return result
}

func (e *Executor) runStep(job *Job, step *Step) (result error) {
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case string:
				result = errors.New(e)
			case error:
				result = e
			default:
				result = errors.New("unknown step error")
			}
		}
	}()

	isWindows := util.IsWindows()

	if job == nil {
		job = &Job{}
	}
	if job.Shell == "" {
		if isWindows {
			job.Shell = "cmd.exe /c"
		} else {
			job.Shell = "/bin/sh"
		}
	}

	shellArgs := strings.Split(job.Shell, " ")

	stepName := e.stepName(step)

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
	} else {
		file, err := os.CreateTemp(os.TempDir(), "*.sh")
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

	if !e.prepareStep(stepName, step) {
		util.LogInfo("[lscbuild] skip step: %s\n", stepName)
		return result
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

	return result
}

func (s *Step) MustCopy() Step {
	// FIXME: slow zone

	b, err := yaml.Marshal(*s)
	if err != nil {
		panic(err)
	}
	step := new(Step)
	if err := yaml.Unmarshal(b, step); err != nil {
		panic(err)
	}
	return *step
}

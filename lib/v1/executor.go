package v1

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/StevenACoffman/simplerr/errors"
	"github.com/common-creation/lscbuild/typing"
	"github.com/common-creation/lscbuild/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/goccy/go-yaml"
	"github.com/joho/godotenv"
	"github.com/mitchellh/mapstructure"
	"github.com/samber/lo"
)

type (
	Executor struct {
		typing.Executor
		lscBuild       *LSCBuild
		globalEnv      []string
		pluginCache    map[string]*Plugin
		tmpBaseDir     string
		tmpEnvFilePath string
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
		Name        *string  `yaml:"name"`
		Use         string   `yaml:"use"`
		Cmd         string   `yaml:"cmd"`
		Dir         string   `yaml:"dir"`
		Env         []string `yaml:"env"`
		If          []If     `yaml:"if"`
		IgnoreError bool     `yaml:"ignore_error" mapstructure:"ignore_error"`
	}
	If struct {
		Directory *IfExists `yaml:"directory,omitempty"`
		File      *IfExists `yaml:"file,omitempty"`
		Env       *[]string `yarl:"env,omitempty"`
		IsError   *bool     `yaml:"is_error,omitempty" mapstructure:"is_error"`
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
		return nil, errors.WithStack(err)
	}

	tmpBaseDir, err := os.MkdirTemp("", "lscbuild")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	tmpEnvFilePath := filepath.Join(tmpBaseDir, ".env")
	file, err := os.Create(tmpEnvFilePath)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer file.Close()

	return &Executor{
		lscBuild:       lscBuild,
		globalEnv:      make([]string, 0),
		pluginCache:    make(map[string]*Plugin),
		tmpBaseDir:     tmpBaseDir,
		tmpEnvFilePath: tmpEnvFilePath,
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
				return errors.WithStack(errors.New("job '" + job + "' is not found"))
			} else {
				targets = append(targets, job)
			}
		}
	}

	for _, job := range e.lscBuild.Jobs {
		if _, ok := lo.Find(targets, func(s string) bool { return s == job.Key }); ok {
			decoded := new(Job)
			if err := mapstructure.Decode(job.Value, decoded); err != nil {
				return errors.WithStack(err)
			}

			util.LogInfo("[lscbuild] initialize plugins\n")
			if err := e.initializePlugins(job.Key.(string), decoded); err != nil {
				return errors.WithStack(err)
			}

			util.LogInfo("[lscbuild] execute jobs\n")
			if err := e.runJob(job.Key.(string), decoded); err != nil {
				return errors.WithStack(err)
			}

			util.LogInfo("[lscbuild] finalize plugins\n")
			if err := e.finalizePlugins(); err != nil {
				return errors.WithStack(err)
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

func (e *Executor) prepareStep(name string, step *Step, env []string) bool {
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
				tt := make([]string, 0)
				tt = append(tt, step.Env...)
				tt = append(tt, env...)

				envMap := make(map[string]string)
				for _, e := range tt {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) == 2 {
						envMap[parts[0]] = parts[1]
					}
				}

				ee := os.Expand(e1, func(s string) string {
					return envMap[s]
				})
				if strings.Contains(ee, "!=") {
					et := strings.Replace(ee, "!=", "=", 1)
					if _, ok := lo.Find(step.Env, func(e2 string) bool { return et == e2 }); ok {
						return false
					}
				} else {
					if _, ok := lo.Find(step.Env, func(e2 string) bool { return ee == e2 }); !ok {
						return false
					}
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
	repoPath := filepath.Join(e.tmpBaseDir, "lscbuild", "plugins")
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
				result = errors.WithStack(errors.New(e))
			case error:
				result = errors.WithStack(e)
			default:
				result = errors.WithStack(errors.New("unknown plugin error"))
			}
		}
	}()

	// NOTE: prepare plugins
	tmpBaseDir := filepath.Join(os.TempDir(), "lscbuild", "plugins")
	os.MkdirAll(tmpBaseDir, os.ModePerm)

	cwd, err := os.Getwd()
	if err != nil {
		panic(errors.WithStack(err))
	}

	for _, step := range job.Steps {
		if step.Use != "" {
			stepName := e.stepName(&step)
			parsed := e.parsePluginUse(step.Use)
			repoPath := e.getRepoPath(parsed.GitRepo)
			if s, err := os.Stat(repoPath); err == nil && s.IsDir() {
				util.LogInfo("[lscbuild] skip already fetched plugin: %s\n", stepName)
				continue
			}

			util.LogInfo("[lscbuild] fetch plugin: %s\n", stepName)

			_, err := git.PlainClone(repoPath, false, &git.CloneOptions{
				URL:           "https://github.com/" + parsed.GitRepo + ".git",
				ReferenceName: plumbing.ReferenceName(parsed.ReferencePrefix + parsed.ReferenceTarget),
				Progress:      os.Stdout,
			})
			if err != nil {
				panic(errors.WithStack(err))
			}

			pluginYamlPath := filepath.Join(repoPath, ".lscbuild.yaml")
			b, err := os.ReadFile(pluginYamlPath)
			if err != nil {
				result = errors.WithStack(err)
				return result
			}
			lscbuild := new(LSCBuild)
			if err := yaml.Unmarshal(b, lscbuild); err != nil {
				result = errors.WithStack(err)
				return result
			}
			e.pluginCache[parsed.GitRepo] = &lscbuild.Plugin
			e.pluginCache[parsed.GitRepo].repoPath = repoPath

			if e.pluginCache[parsed.GitRepo].LifeCycle.Initialize != nil {
				util.LogInfo("[lscbuild] start initialize plugin: %s\n", stepName)

				initializeStep := e.pluginCache[parsed.GitRepo].LifeCycle.Initialize.MustCopy()
				initializeStep.Dir = repoPath
				initializeStep.Env = append(initializeStep.Env, "LSCBUILD_CWD="+cwd)
				initializeStep.Env = append(initializeStep.Env, "LSCBUILD_ENV_FILE="+e.tmpEnvFilePath)
				if envMap, err := godotenv.Read(e.tmpEnvFilePath); err == nil {
					for key, value := range envMap {
						initializeStep.Env = append(initializeStep.Env, key+"="+value)
					}
				}

				if err := e.runStep(&e.pluginCache[parsed.GitRepo].Job, &initializeStep); err != nil {
					result = errors.WithStack(err)
					return result
				}
				util.LogInfo("[lscbuild] end initialize plugin: %s\n", stepName)
			}
		}
	}

	return result
}

func (e *Executor) finalizePlugins() (result error) {
	cwd, err := os.Getwd()
	if err != nil {
		panic(errors.WithStack(err))
	}

	for _, v := range e.pluginCache {
		if v.LifeCycle.Finalize != nil {
			finalizeStep := v.LifeCycle.Finalize.MustCopy()
			stepName := e.stepName(&finalizeStep)
			util.LogInfo("[lscbuild] start finalize plugin: %s\n", stepName)

			finalizeStep.Dir = v.repoPath
			finalizeStep.Env = append(finalizeStep.Env, "LSCBUILD_CWD="+cwd)
			finalizeStep.Env = append(finalizeStep.Env, "LSCBUILD_ENV_FILE="+e.tmpEnvFilePath)
			if envMap, err := godotenv.Read(e.tmpEnvFilePath); err == nil {
				for key, value := range envMap {
					finalizeStep.Env = append(finalizeStep.Env, key+"="+value)
				}
			}

			defer func() {
				os.RemoveAll(v.repoPath)
			}()
			if err := e.runStep(&v.Job, &finalizeStep); err != nil {
				result = errors.WithStack(err)
				return result
			}
			util.LogInfo("[lscbuild] end finalize plugin: %s\n", stepName)
		}
	}

	return result
}

func (e *Executor) runJob(name string, job *Job) (result error) {
	util.LogInfo("[lscbuild] start job: %s\n", name)
	util.LogDebug("[lscbuild] job: %+v\n", job)
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case string:
				result = errors.WithStack(errors.New(e))
			case error:
				result = errors.WithStack(e)
			default:
				result = errors.WithStack(errors.New("unknown job error"))
			}
		}
	}()

	isWindows := util.IsWindows()

	if job.Shell == "" {
		if isWindows {
			job.Shell = "powershell.exe"
		} else {
			job.Shell = "/bin/sh -e"
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		panic(errors.WithStack(err))
	}

	for _, step := range job.Steps {
		if result != nil && !step.IsError() {
			util.LogInfo("[lscbuild] skip step: %s\n", e.stepName(&step))
			continue
		}
		if result == nil && step.IsError() {
			util.LogInfo("[lscbuild] skip step: %s\n", e.stepName(&step))
			continue
		}

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
				// NOTE: .envはrunStep内で読み込む

				result = e.runStep(job, &newStep)
			}
		} else {
			result = e.runStep(job, &step)
		}

		if step.IgnoreError {
			result = nil
		}
	}

	return result
}

func (e *Executor) runStep(job *Job, step *Step) (result error) {
	defer func() {
		if err := recover(); err != nil {
			switch e := err.(type) {
			case string:
				result = errors.WithStack(errors.New(e))
			case error:
				result = errors.WithStack(e)
			default:
				result = errors.WithStack(errors.New("unknown step error"))
			}
		}
	}()

	isWindows := util.IsWindows()

	if job == nil {
		job = &Job{}
	}
	if job.Shell == "" {
		if isWindows {
			job.Shell = "powershell.exe"
		} else {
			job.Shell = "/bin/sh -e"
		}
	}
	if isWindows && strings.HasPrefix(job.Shell, "cmd.exe") {
		job.Shell = "cmd.exe /c"
	}

	shellArgs := strings.Split(job.Shell, " ")

	stepName := e.stepName(step)

	if isWindows {
		if strings.Contains(job.Shell, "cmd") {
			file, err := os.CreateTemp(e.tmpBaseDir, "*.bat")
			if err != nil {
				panic(errors.WithStack(err))
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
			file, err := os.CreateTemp(e.tmpBaseDir, "*.ps1")
			if err != nil {
				panic(errors.WithStack(err))
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
		file, err := os.CreateTemp(e.tmpBaseDir, "*.sh")
		if err != nil {
			panic(errors.WithStack(err))
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
	if isWindows {
		env = append(env, "ErrorActionPreference=Stop")
	}
	env = append(env, "LSCBUILD_ENV_FILE="+e.tmpEnvFilePath)
	if envMap, err := godotenv.Read(e.tmpEnvFilePath); err == nil {
		for key, value := range envMap {
			env = append(env, key+"="+value)
		}
	} else {
		util.LogInfo("[lscbuild] read env file error: %w\n", errors.WithStack(err))
	}

	cmd.Env = env
	step.Env = env

	if !e.prepareStep(stepName, step, env) {
		util.LogInfo("[lscbuild] skip step: %s\n", stepName)
		return result
	}

	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	util.LogInfo("[lscbuild] start step: %s\n", stepName)
	defer func() {
		util.LogInfo("[lscbuild] end step: %s, exit status: %d\n", stepName, cmd.ProcessState.ExitCode())
	}()

	if err := cmd.Start(); err != nil {
		result = errors.WithStack(err)
		return result
	}
	if err := cmd.Wait(); err != nil {
		result = errors.WithStack(err)
		return result
	}

	return result
}

func (s *Step) IsError() bool {
	if s.If == nil {
		return false
	}
	for _, i := range s.If {
		if i.IsError != nil && *i.IsError {
			return true
		}
	}
	return false
}

func (s *Step) MustCopy() Step {
	// FIXME: slow zone

	b, err := yaml.Marshal(*s)
	if err != nil {
		panic(errors.WithStack(err))
	}
	step := new(Step)
	if err := yaml.Unmarshal(b, step); err != nil {
		panic(errors.WithStack(err))
	}
	return *step
}

package typing

type (
	Executor interface {
		Run(jobs ...string) error
	}
)

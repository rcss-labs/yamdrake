package awstest

import (
	"errors"
	"fmt"
)

// Program is a TB for a program (not a test) that seeds Floci: a failure
// becomes the error Run returns, and cleanups run when Close is called.
type Program struct {
	cleanups []func()
}

type programFailure string

// Helper does nothing: a program has no test frames to hide.
func (p *Program) Helper() {}

// Fatalf stops the function given to Run and makes Run return the message.
func (p *Program) Fatalf(format string, args ...any) {
	panic(programFailure(fmt.Sprintf(format, args...)))
}

// Cleanup registers f to run, last registered first, when Close is called.
func (p *Program) Cleanup(f func()) { p.cleanups = append(p.cleanups, f) }

// Close runs the registered cleanups. They remove only local images the seeding built.
func (p *Program) Close() {
	for i := len(p.cleanups) - 1; i >= 0; i-- {
		p.cleanups[i]()
	}
	p.cleanups = nil
}

// Run calls f and returns the failure it reported with Fatalf as an error.
// Any other panic is not caught.
func (p *Program) Run(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			failure, ok := r.(programFailure)
			if !ok {
				panic(r)
			}
			err = errors.New(string(failure))
		}
	}()
	f()
	return nil
}

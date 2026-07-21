package check

import (
	"fmt"

	"github.com/rewantsoni/dr-network-check/pkg/console"
)

type Status string

const (
	StatusPass Status = "PASS"
	StatusFail Status = "FAIL"
	StatusWarn Status = "WARN"
	StatusSkip Status = "SKIP"
)

type CheckResult struct {
	Name    string
	Status  Status
	Message string
}

type CheckReport struct {
	Results []CheckResult
}

func (r *CheckReport) Add(results ...CheckResult) {
	r.Results = append(r.Results, results...)
}

func (r *CheckReport) Print() {
	var pass, fail, warn, skip int

	for _, res := range r.Results {
		switch res.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusWarn:
			warn++
		case StatusSkip:
			skip++
		}
	}

	if fail > 0 {
		console.Info("Failures:")

		for _, res := range r.Results {
			if res.Status == StatusFail {
				console.Fail("%s", res.Message)
			}
		}
	}

	if warn > 0 {
		console.Info("Warnings:")

		for _, res := range r.Results {
			if res.Status == StatusWarn {
				console.Warn("%s", res.Message)
			}
		}
	}

	fmt.Println()
	console.Info("Summary: %d passed, %d failed, %d warnings, %d skipped",
		pass, fail, warn, skip)
}

func (r *CheckReport) HasFailures() bool {
	for _, res := range r.Results {
		if res.Status == StatusFail {
			return true
		}
	}

	return false
}

package check

import "testing"

func TestCheckReportAdd(t *testing.T) {
	r := &CheckReport{}

	r.Add(CheckResult{Name: "a", Status: StatusPass})
	if len(r.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(r.Results))
	}

	r.Add(
		CheckResult{Name: "b", Status: StatusFail},
		CheckResult{Name: "c", Status: StatusWarn},
	)
	if len(r.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(r.Results))
	}
}

func TestCheckReportHasFailures(t *testing.T) {
	tests := []struct {
		name    string
		results []CheckResult
		want    bool
	}{
		{"empty report", nil, false},
		{"only pass", []CheckResult{{Status: StatusPass}}, false},
		{"only warn", []CheckResult{{Status: StatusWarn}}, false},
		{"only skip", []CheckResult{{Status: StatusSkip}}, false},
		{"has fail", []CheckResult{{Status: StatusFail}}, true},
		{
			"mixed with fail",
			[]CheckResult{
				{Status: StatusPass},
				{Status: StatusWarn},
				{Status: StatusFail},
			},
			true,
		},
		{
			"mixed without fail",
			[]CheckResult{
				{Status: StatusPass},
				{Status: StatusWarn},
				{Status: StatusSkip},
			},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &CheckReport{Results: tt.results}
			got := r.HasFailures()
			if got != tt.want {
				t.Errorf("HasFailures() = %v, want %v", got, tt.want)
			}
		})
	}
}

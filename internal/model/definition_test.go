package model

import (
	"testing"
)

func TestProcessDefinition_Validate(t *testing.T) {
	validTask := &Step{
		Type:      StepTypeTask,
		ID:        "step1",
		Transport: TransportHTTP,
		Endpoint:  "http://localhost/action",
	}

	tests := []struct {
		name    string
		def     ProcessDefinition
		wantErr string
	}{
		{
			name:    "valid definition",
			def:     ProcessDefinition{Name: "p", Version: 1, Steps: []*Step{validTask}},
			wantErr: "",
		},
		{
			name:    "missing name",
			def:     ProcessDefinition{Version: 1, Steps: []*Step{validTask}},
			wantErr: "name is required",
		},
		{
			name:    "version zero",
			def:     ProcessDefinition{Name: "p", Version: 0, Steps: []*Step{validTask}},
			wantErr: "version must have at least 1 item(s)",
		},
		{
			name:    "no steps",
			def:     ProcessDefinition{Name: "p", Version: 1},
			wantErr: "steps",
		},
		{
			name: "task missing endpoint",
			def: ProcessDefinition{Name: "p", Version: 1, Steps: []*Step{
				{Type: StepTypeTask, ID: "s1", Transport: TransportHTTP},
			}},
			wantErr: "endpoint is required",
		},
		{
			name: "task unknown transport",
			def: ProcessDefinition{Name: "p", Version: 1, Steps: []*Step{
				{Type: StepTypeTask, ID: "s1", Transport: "ftp", Endpoint: "ftp://x"},
			}},
			wantErr: "transport must be one of: http, tcp, uds",
		},
		{
			name: "conditional missing condition",
			def: ProcessDefinition{Name: "p", Version: 1, Steps: []*Step{
				{Type: StepTypeConditional, ID: "c1"},
			}},
			wantErr: "condition is required",
		},
		{
			name: "unknown step type",
			def: ProcessDefinition{Name: "p", Version: 1, Steps: []*Step{
				{Type: "parallel", ID: "p1"},
			}},
			wantErr: "type must be one of: task, conditional",
		},
		{
			name: "nested step invalid",
			def: ProcessDefinition{Name: "p", Version: 1, Steps: []*Step{
				{
					Type: StepTypeConditional, ID: "c1", Condition: "context.ok == true",
					Then: []*Step{{Type: StepTypeTask, ID: "t1"}}, // missing transport+endpoint
				},
			}},
			wantErr: "endpoint is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.def.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tt.wantErr)
				return
			}
			if !containsStr(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := range s {
		if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

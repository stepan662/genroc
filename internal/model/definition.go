package model

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

// StepType identifies the kind of step.
type StepType string

const (
	StepTypeTask        StepType = "task"
	StepTypeConditional StepType = "conditional"
)

// Transport identifies how the engine communicates with a service.
type Transport string

const (
	TransportHTTP Transport = "http"
	TransportTCP  Transport = "tcp"
	TransportUDS  Transport = "uds"
)

// Step is a single unit of work in a process definition.
// Validation rules are expressed as struct tags — the constraints live next to the fields.
type Step struct {
	Type      StepType  `json:"type"                validate:"required,oneof=task conditional"`
	ID        string    `json:"id"                  validate:"required"`
	Transport Transport `json:"transport,omitempty" validate:"required_if=Type task,omitempty,oneof=http tcp uds"`
	Endpoint  string    `json:"endpoint,omitempty"  validate:"required_if=Type task"`
	TimeoutMs int       `json:"timeout_ms,omitempty"`
	Retries   int       `json:"retries,omitempty"`
	Condition string    `json:"condition,omitempty" validate:"required_if=Type conditional"`
	Then      []*Step   `json:"then,omitempty"      validate:"omitempty,dive"`
	Else      []*Step   `json:"else,omitempty"      validate:"omitempty,dive"`
}

// ProcessDefinition is the immutable versioned blueprint for a process.
// Once published, a version must never be mutated — create a new version instead.
type ProcessDefinition struct {
	Name    string  `json:"name"    validate:"required"`
	Version int     `json:"version" validate:"min=1"`
	Steps   []*Step `json:"steps"   validate:"required,min=1,dive"`
}

// Validate checks the definition and all its steps against the struct tag rules.
func (d *ProcessDefinition) Validate() error {
	return fmtValidationErr(v.Struct(d))
}

// v is the shared validator, configured to report JSON field names in errors.
var v = func() *validator.Validate {
	val := validator.New()
	val.RegisterTagNameFunc(func(f reflect.StructField) string {
		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		if name == "-" || name == "" {
			return f.Name
		}
		return name
	})
	return val
}()

// fmtValidationErr converts validator.ValidationErrors into a readable API error.
func fmtValidationErr(err error) error {
	if err == nil {
		return nil
	}
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return err
	}
	msgs := make([]string, len(ve))
	for i, fe := range ve {
		msgs[i] = describeFieldErr(fe)
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

func describeFieldErr(fe validator.FieldError) string {
	field := fe.Field()
	switch fe.Tag() {
	case "required", "required_if":
		return fmt.Sprintf("%s is required", field)
	case "min":
		return fmt.Sprintf("%s must have at least %s item(s)", field, fe.Param())
	case "oneof":
		return fmt.Sprintf("%s must be one of: %s", field, strings.ReplaceAll(fe.Param(), " ", ", "))
	default:
		return fe.Error()
	}
}

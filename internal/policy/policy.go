package policy

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/open-policy-agent/opa/v1/rego"
)

//go:embed mvp.rego
var MVPModule string

func NewMVP(ctx context.Context) (*OPAEvaluator, error) {
	return NewOPA(ctx, "mvp1-v1", "data.sovereign.decision", MVPModule)
}

type Decision struct {
	ID      string   `json:"id"`
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons,omitempty"`
	Evict   []string `json:"evict,omitempty"`
}

type Evaluator interface {
	Evaluate(context.Context, any) (Decision, error)
}

type OPAEvaluator struct {
	revision string
	query    rego.PreparedEvalQuery
}

func NewOPA(ctx context.Context, revision, query, module string) (*OPAEvaluator, error) {
	prepared, err := rego.New(rego.Query(query), rego.Module("sovereign.rego", module)).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("prepare OPA policy: %w", err)
	}
	return &OPAEvaluator{revision: revision, query: prepared}, nil
}

func (o *OPAEvaluator) Evaluate(ctx context.Context, input any) (Decision, error) {
	results, err := o.query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return Decision{}, fmt.Errorf("evaluate OPA policy: %w", err)
	}
	if len(results) != 1 || len(results[0].Expressions) != 1 {
		return Decision{}, fmt.Errorf("OPA policy returned no single decision")
	}
	raw, ok := results[0].Expressions[0].Value.(map[string]any)
	if !ok {
		return Decision{}, fmt.Errorf("OPA decision must be an object")
	}
	decision := Decision{ID: o.revision}
	if allowed, ok := raw["allowed"].(bool); ok {
		decision.Allowed = allowed
	}
	decision.Reasons = stringSlice(raw["reasons"])
	decision.Evict = stringSlice(raw["evict"])
	return decision, nil
}

func stringSlice(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

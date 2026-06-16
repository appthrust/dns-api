package celruntime

import (
	"fmt"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	krocel "github.com/kubernetes-sigs/kro/pkg/cel"
	kroconversion "github.com/kubernetes-sigs/kro/pkg/cel/conversion"
	"github.com/kubernetes-sigs/kro/pkg/cel/sentinels"
)

// Environment returns the dns-api CEL environment. It intentionally uses KRO's
// base environment so provider expressions have the same libraries as KRO.
func Environment(declarations ...cel.EnvOption) (*cel.Env, error) {
	return krocel.DefaultEnvironment(krocel.WithCustomDeclarations(declarations))
}

func CompileBool(rule string, declarations ...cel.EnvOption) error {
	env, err := Environment(declarations...)
	if err != nil {
		return err
	}
	ast, issues := env.Compile(rule)
	if issues != nil && issues.Err() != nil {
		return issues.Err()
	}
	if !cel.BoolType.IsAssignableType(ast.OutputType()) {
		return fmt.Errorf("CEL expression must return bool, got %s", ast.OutputType())
	}
	return nil
}

func CompileObject(rule string, declarations ...cel.EnvOption) error {
	env, err := Environment(declarations...)
	if err != nil {
		return err
	}
	ast, issues := env.Compile(rule)
	if issues != nil && issues.Err() != nil {
		return issues.Err()
	}
	switch ast.OutputType().Kind() {
	case celtypes.MapKind, celtypes.StructKind, celtypes.DynKind:
		return nil
	default:
		return fmt.Errorf("CEL expression must return object, got %s", ast.OutputType())
	}
}

func EvalBool(rule string, activation map[string]interface{}, declarations ...cel.EnvOption) (bool, error) {
	env, err := Environment(declarations...)
	if err != nil {
		return false, err
	}
	ast, issues := env.Compile(rule)
	if issues != nil && issues.Err() != nil {
		return false, issues.Err()
	}
	if !cel.BoolType.IsAssignableType(ast.OutputType()) {
		return false, fmt.Errorf("CEL expression must return bool, got %s", ast.OutputType())
	}
	program, err := env.Program(ast)
	if err != nil {
		return false, err
	}
	out, _, err := program.Eval(activation)
	if err != nil {
		return false, err
	}
	value, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression must return bool")
	}
	return value, nil
}

func EvalObject(rule string, activation map[string]interface{}, declarations ...cel.EnvOption) (map[string]interface{}, error) {
	env, err := Environment(declarations...)
	if err != nil {
		return nil, err
	}
	ast, issues := env.Compile(rule)
	if issues != nil && issues.Err() != nil {
		return nil, issues.Err()
	}
	switch ast.OutputType().Kind() {
	case celtypes.MapKind, celtypes.StructKind, celtypes.DynKind:
	default:
		return nil, fmt.Errorf("CEL expression must return object, got %s", ast.OutputType())
	}
	program, err := env.Program(ast)
	if err != nil {
		return nil, err
	}
	out, _, err := program.Eval(activation)
	if err != nil {
		return nil, err
	}
	native, err := kroconversion.GoNativeType(out)
	if err != nil {
		return nil, err
	}
	cleaned := CleanOmitSentinels(native)
	object, ok := cleaned.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("CEL expression must return object, got %T", cleaned)
	}
	return object, nil
}

func CleanOmitSentinels(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if sentinels.IsOmit(child) {
				delete(typed, key)
				continue
			}
			typed[key] = CleanOmitSentinels(child)
		}
		return typed
	case []interface{}:
		filtered := make([]interface{}, 0, len(typed))
		for _, child := range typed {
			if !sentinels.IsOmit(child) {
				filtered = append(filtered, CleanOmitSentinels(child))
			}
		}
		return filtered
	default:
		return value
	}
}

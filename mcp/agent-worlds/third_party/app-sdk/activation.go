package sdk

import "fmt"

const (
	ActivationAuto      = "auto"
	ActivationExclusive = "exclusive"
)

// ValidateActivationStrategy also applies to manifests restored from JSON,
// which do not pass through ParseManifest.
func ValidateActivationStrategy(runtime Runtime) error {
	switch runtime.ActivationStrategy {
	case "", ActivationAuto:
		return nil
	case ActivationExclusive:
		if runtime.Kind == "static" {
			return fmt.Errorf("runtime.activation_strategy exclusive requires a process runtime")
		}
		return nil
	default:
		return fmt.Errorf("runtime.activation_strategy must be auto or exclusive (got %q)", runtime.ActivationStrategy)
	}
}

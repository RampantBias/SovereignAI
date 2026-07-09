package orchestration

import (
	"errors"
)

type ModelSpecs struct {
	Name      string `yaml:"name"`
	MinVramMB int    `yaml:"min_vram_mb"`
}

func (c *SovereignWorkflowController) GetModelVramRequirement(modelName string) (int, error) {
	// 1. In a real app, you'd load o.config.ModelRegistry (a map)
	// For now, we'll mock the lookup
	// for _, m := range o.modelConfig {
	// 	if m.Name == modelName {
	// 		return m.RequiredVRAM, nil
	// 	}
	// }

	return 0, errors.New("Model not found in models.yaml configuration")
}

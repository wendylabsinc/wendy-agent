package cdi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultCDISpecPath = "/etc/cdi"

// Manager loads and manages CDI specifications.
type Manager struct {
	specPath string
}

func NewManager() *Manager {
	return &Manager{specPath: defaultCDISpecPath}
}

// LoadNVIDIACDISpec loads the NVIDIA CDI spec from YAML.
// It tries /var/run/cdi/nvidia.yaml first, then /etc/cdi/nvidia.yaml.
func (m *Manager) LoadNVIDIACDISpec() (*CDISpecification, error) {
	possiblePaths := []string{
		"/var/run/cdi/nvidia.yaml",
		filepath.Join(m.specPath, "nvidia.yaml"),
	}

	var specPath string
	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			specPath = path
			break
		}
	}

	if specPath == "" {
		return nil, &CDIError{
			Message: fmt.Sprintf("CDI spec not found at %s", strings.Join(possiblePaths, ", ")),
		}
	}

	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, &CDIError{
			Message: fmt.Sprintf("cannot read CDI spec at %s: %v", specPath, err),
		}
	}

	var spec CDISpecification
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing NVIDIA CDI YAML spec: %w", err)
	}

	return &spec, nil
}

//go:build linux && !cgo && cli

package subscription

import (
	"github.com/metacubex/mihomo/config"
	"gopkg.in/yaml.v3"
)

func tuiYAMLMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func validateConfigBytes(data []byte) string {
	if _, err := config.UnmarshalRawConfig(data); err != nil {
		return err.Error()
	}
	return ""
}

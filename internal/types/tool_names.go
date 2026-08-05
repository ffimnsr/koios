package types

import (
	"fmt"
	"strconv"
)

// EncodeProviderToolName converts Koios canonical tool names to provider-safe
// function names. OpenAI and Anthropic reject characters outside letters,
// digits, underscores, and dashes, while Koios uses dotted domains such as
// tool.list internally. The escape is reversible and also escapes literal
// _xHH_ sequences to avoid collisions.
func EncodeProviderToolName(name string) string {
	if name == "" {
		return name
	}
	encoded := make([]byte, 0, len(name))
	changed := false
	for i := 0; i < len(name); i++ {
		b := name[i]
		if isProviderToolNameByte(b) && !startsProviderEscape(name, i) {
			encoded = append(encoded, b)
			continue
		}
		encoded = append(encoded, fmt.Sprintf("_x%02x_", b)...)
		changed = true
	}
	if !changed {
		return name
	}
	return string(encoded)
}

// DecodeProviderToolName reverses EncodeProviderToolName. Names without escape
// sequences are returned unchanged so already-safe provider names still work.
func DecodeProviderToolName(name string) string {
	if name == "" {
		return name
	}
	decoded := make([]byte, 0, len(name))
	changed := false
	for i := 0; i < len(name); i++ {
		if !startsProviderEscape(name, i) {
			decoded = append(decoded, name[i])
			continue
		}
		value, err := strconv.ParseUint(name[i+2:i+4], 16, 8)
		if err != nil {
			decoded = append(decoded, name[i])
			continue
		}
		decoded = append(decoded, byte(value))
		i += 4
		changed = true
	}
	if !changed {
		return name
	}
	return string(decoded)
}

func isProviderToolNameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '-'
}

func startsProviderEscape(name string, i int) bool {
	return i+4 < len(name) &&
		name[i] == '_' &&
		name[i+1] == 'x' &&
		isHexByte(name[i+2]) &&
		isHexByte(name[i+3]) &&
		name[i+4] == '_'
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

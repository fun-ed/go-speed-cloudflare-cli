package main

import (
	"strings"
	"testing"
)

func TestColorFunctions(t *testing.T) {
	tests := []struct {
		name     string
		function func(...interface{}) string
		input    string
		prefix   string
	}{
		{"Magenta", Magenta, "test", "\033[35m"},
		{"Bold", Bold, "test", "\033[1m"},
		{"Yellow", Yellow, "test", "\033[33m"},
		{"Green", Green, "test", "\033[32m"},
		{"Blue", Blue, "test", "\033[34m"},
		{"Cyan", Cyan, "test", "\033[36m"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.function(test.input)

			// Check that result starts with the correct ANSI color code
			if !strings.HasPrefix(result, test.prefix) {
				t.Errorf("%s(%q) = %q; expected to start with %q",
					test.name, test.input, result, test.prefix)
			}

			// Check that result contains the input string
			if !strings.Contains(result, test.input) {
				t.Errorf("%s(%q) = %q; expected to contain %q",
					test.name, test.input, result, test.input)
			}

			// Check that result ends with the reset code
			if !strings.HasSuffix(result, "\033[0m") {
				t.Errorf("%s(%q) = %q; expected to end with reset code",
					test.name, test.input, result)
			}
		})
	}
}

func TestColorMultipleArguments(t *testing.T) {
	// Test with multiple arguments
	result := Green("speed:", 42, "Mbps")
	expected := "\033[32mspeed:42Mbps\033[0m"

	if result != expected {
		t.Errorf("Green(\"speed:\", 42, \"Mbps\") = %q; expected %q",
			result, expected)
	}
}

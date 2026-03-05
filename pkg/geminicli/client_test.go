package geminicli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecute tests the main function for executing Gemini commands
func TestExecute(t *testing.T) {
	tests := []struct {
		name        string
		prompt      string
		expectError bool
	}{
		{
			name:        "EmptyPrompt",
			prompt:      "",
			expectError: true,
		},
		{
			name:        "ValidPrompt",
			prompt:      "test",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup a mock gemini command
			tmpDir, err := os.MkdirTemp("", "gemini-mock")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			mockPath := filepath.Join(tmpDir, "gemini")
			mockContent := `#!/bin/bash
echo '{"response": "Hello", "stats": {"models": {"gemini-3-flash-preview": {"tokens": {"total": 10}}}}}'
`
			if err := os.WriteFile(mockPath, []byte(mockContent), 0755); err != nil {
				t.Fatalf("Failed to create mock gemini: %v", err)
			}

			// Update PATH to include our mock
			originalPath := os.Getenv("PATH")
			os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+originalPath)
			defer os.Setenv("PATH", originalPath)

			client := NewClient()
			ctx := context.Background()
			result, err := client.Execute(ctx, tt.prompt)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for test case '%s', but got none", tt.name)
				}
			} else {
				if err != nil {
					// Skip if gemini not found in real environment if mock failed
					if strings.Contains(err.Error(), "executable file not found") {
						t.Skip("Gemini command not available")
					}
					t.Errorf("Unexpected error for test case '%s': %v", tt.name, err)
				}
				if result == "" && err == nil {
					t.Errorf("Expected non-empty result for test case '%s'", tt.name)
				}
			}
		})
	}
}

// TestExecuteDetailed tests token usage reporting
func TestExecuteDetailed(t *testing.T) {
	// Setup a mock gemini command
	tmpDir, err := os.MkdirTemp("", "gemini-mock-detailed")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockPath := filepath.Join(tmpDir, "gemini")
	mockContent := `#!/bin/bash
echo '{"response": "Hello Detailed", "stats": {"models": {"gemini-3-flash-preview": {"tokens": {"input": 5, "candidates": 10, "total": 15}}}}}'
`
	if err := os.WriteFile(mockPath, []byte(mockContent), 0755); err != nil {
		t.Fatalf("Failed to create mock gemini: %v", err)
	}

	// Update PATH to include our mock
	originalPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+originalPath)
	defer os.Setenv("PATH", originalPath)

	client := NewClient()
	resp, err := client.ExecuteDetailed(context.Background(), "test")
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skip("Gemini command not available")
		}
		t.Fatalf("ExecuteDetailed failed: %v", err)
	}

	if resp.Response != "Hello Detailed" {
		t.Errorf("Expected 'Hello Detailed', got '%s'", resp.Response)
	}
	if resp.TokenUsage.Total != 15 {
		t.Errorf("Expected 15 tokens, got %d", resp.TokenUsage.Total)
	}
}

// TestParseDetailedOutput tests JSON parsing
func TestParseDetailedOutput(t *testing.T) {
	client := NewClient()
	jsonIn := []byte(`{"response": "Hello", "stats": {"models": {"gemini-3-flash-preview": {"tokens": {"total": 42}}}}}`)

	resp, err := client.parseDetailedOutput(jsonIn)
	if err != nil {
		t.Fatalf("parseDetailedOutput failed: %v", err)
	}

	if resp.Response != "Hello" {
		t.Errorf("Expected 'Hello', got '%s'", resp.Response)
	}
	if resp.TokenUsage.Total != 42 {
		t.Errorf("Expected 42 tokens, got %d", resp.TokenUsage.Total)
	}
}

// TestParseDetailedOutput_Resilience tests extraction from noisy output
func TestParseDetailedOutput_Resilience(t *testing.T) {
	client := NewClient()
	noisyIn := []byte(`Loaded cached credentials.
{
  "response": "Hello with noise",
  "stats": {
    "models": {
      "gemini-3-flash-preview": {
        "tokens": {
          "total": 100
        }
      }
    }
  }
}
Some trailing noise.`)

	resp, err := client.parseDetailedOutput(noisyIn)
	if err != nil {
		t.Fatalf("parseDetailedOutput failed: %v", err)
	}

	if resp.Response != "Hello with noise" {
		t.Errorf("Expected 'Hello with noise', got '%s'", resp.Response)
	}
	if resp.TokenUsage.Total != 100 {
		t.Errorf("Expected 100 tokens, got %d", resp.TokenUsage.Total)
	}
}

// TestDetectAuthError tests authentication error detection
func TestDetectAuthError(t *testing.T) {
	client := NewClient()
	tests := []struct {
		name        string
		output      string
		expectAuth  bool
	}{
		{
			name:        "NoAuthError",
			output:      "Normal Gemini response",
			expectAuth:  false,
		},
		{
			name:        "AuthenticationError",
			output:      "Error: authentication failed",
			expectAuth:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.detectAuthError(tt.output)
			if result != tt.expectAuth {
				t.Errorf("Expected %v, got %v", tt.expectAuth, result)
			}
		})
	}
}

// TestNewClient tests client creation
func TestNewClient(t *testing.T) {
	client := NewClient()
	if client == nil {
		t.Error("NewClient should return a valid client")
	}
}

// TestNoOpLogger tests the no-op logger implementation
func TestNoOpLogger(t *testing.T) {
	logger := NewNoOpLogger()

	// These should not panic
	logger.DebugWith("test", "key", "value")
	logger.InfoWith("test", "key", "value")
	logger.WarnWith("test", "key", "value")
	logger.ErrorWith("test", "key", "value")
}

// TestResolveRelativePaths tests the relative path resolution functionality
func TestResolveRelativePaths(t *testing.T) {
	client := NewClient()
	tests := []struct {
		name     string
		prompt   string
		baseDir  string
		expected string
	}{
		{
			name:     "RelativePathWithDot",
			prompt:   "Analyze ./main.go",
			baseDir:  "/project/src",
			expected: "Analyze /project/src/main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.resolveRelativePaths(tt.prompt, tt.baseDir)
			if result != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, result)
			}
		})
	}
}

// TestExecuteWithNoSessionFallback tests the session fallback logic
func TestExecuteWithNoSessionFallback(t *testing.T) {
	// Setup a mock gemini command
	tmpDir, err := os.MkdirTemp("", "gemini-mock-session")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockPath := filepath.Join(tmpDir, "gemini")

	// This mock script will fail if -r latest is passed, but succeed otherwise.
	// It simulates the "No previous sessions found" error.
	mockContent := `#!/bin/bash
has_resume=false
for arg in "$@"; do
    if [ "$arg" == "-r" ]; then
        has_resume=true
    fi
done

if [ "$has_resume" = true ]; then
    echo "Error resuming session: No previous sessions found for this project." >&2
    exit 1
else
    echo '{"response": "Fallback Response", "stats": {"models": {"gemini-3-flash-preview": {"tokens": {"total": 10}}}}}'
fi
`
	if err := os.WriteFile(mockPath, []byte(mockContent), 0755); err != nil {
		t.Fatalf("Failed to create mock gemini: %v", err)
	}

	// Update PATH to include our mock
	originalPath := os.Getenv("PATH")
	os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+originalPath)
	defer os.Setenv("PATH", originalPath)

	client := NewClient()
	ctx := context.Background()

	// ExecuteDetailed should try with -r latest first, fail, and then try without it.
	resp, err := client.ExecuteDetailed(ctx, "test prompt")
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			t.Skip("Gemini command not available")
		}
		t.Fatalf("Unexpected error: %v", err)
	}

	if resp.Response != "Fallback Response" {
		t.Errorf("Expected 'Fallback Response', got '%s'", resp.Response)
	}
}

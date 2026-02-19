package opencode

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsurePluginAt_EmptyParameters(t *testing.T) {
	// Test that empty pluginDir or pluginFile returns nil
	t.Run("empty pluginDir", func(t *testing.T) {
		err := EnsurePluginAt("/tmp/work", "", "plugin.js")
		if err != nil {
			t.Errorf("EnsurePluginAt() with empty pluginDir should return nil, got %v", err)
		}
	})

	t.Run("empty pluginFile", func(t *testing.T) {
		err := EnsurePluginAt("/tmp/work", "plugins", "")
		if err != nil {
			t.Errorf("EnsurePluginAt() with empty pluginFile should return nil, got %v", err)
		}
	})

	t.Run("both empty", func(t *testing.T) {
		err := EnsurePluginAt("/tmp/work", "", "")
		if err != nil {
			t.Errorf("EnsurePluginAt() with both empty should return nil, got %v", err)
		}
	})
}

func TestEnsurePluginAt_FileExists(t *testing.T) {
	t.Run("overwrites stale content", func(t *testing.T) {
		tmpDir := t.TempDir()
		pluginDir := "plugins"
		pluginFile := "gastown.js"
		pluginPath := filepath.Join(tmpDir, pluginDir, pluginFile)

		if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
			t.Fatalf("Failed to create test directory: %v", err)
		}

		// Write stale content that differs from the embedded version
		staleContent := []byte("// old plugin version")
		if err := os.WriteFile(pluginPath, staleContent, 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}

		err := EnsurePluginAt(tmpDir, pluginDir, pluginFile)
		if err != nil {
			t.Fatalf("EnsurePluginAt() error = %v", err)
		}

		// File should have been overwritten with the embedded version
		content, err := os.ReadFile(pluginPath)
		if err != nil {
			t.Fatalf("Failed to read plugin file: %v", err)
		}
		if string(content) == string(staleContent) {
			t.Error("EnsurePluginAt() should overwrite stale file with embedded version")
		}
		if len(content) == 0 {
			t.Error("Plugin file should have content after sync")
		}
	})

	t.Run("skips write when content matches", func(t *testing.T) {
		tmpDir := t.TempDir()
		pluginDir := "plugins"
		pluginFile := "gastown.js"
		pluginPath := filepath.Join(tmpDir, pluginDir, pluginFile)

		// First call creates the file with embedded content
		err := EnsurePluginAt(tmpDir, pluginDir, pluginFile)
		if err != nil {
			t.Fatalf("EnsurePluginAt() first call error = %v", err)
		}

		// Record the mod time
		info1, err := os.Stat(pluginPath)
		if err != nil {
			t.Fatalf("Failed to stat plugin file: %v", err)
		}

		// Second call should be a no-op (content matches)
		err = EnsurePluginAt(tmpDir, pluginDir, pluginFile)
		if err != nil {
			t.Fatalf("EnsurePluginAt() second call error = %v", err)
		}

		info2, err := os.Stat(pluginPath)
		if err != nil {
			t.Fatalf("Failed to stat plugin file: %v", err)
		}

		if info2.ModTime() != info1.ModTime() {
			t.Error("EnsurePluginAt() should not rewrite file when content matches")
		}
	})
}

func TestEnsurePluginAt_CreatesFile(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()

	pluginDir := "plugins"
	pluginFile := "gastown.js"
	pluginPath := filepath.Join(tmpDir, pluginDir, pluginFile)

	// Ensure plugin doesn't exist
	if _, err := os.Stat(pluginPath); err == nil {
		t.Fatal("Plugin file should not exist yet")
	}

	// Create the plugin
	err := EnsurePluginAt(tmpDir, pluginDir, pluginFile)
	if err != nil {
		t.Fatalf("EnsurePluginAt() error = %v", err)
	}

	// Verify file was created
	info, err := os.Stat(pluginPath)
	if err != nil {
		t.Fatalf("Plugin file was not created: %v", err)
	}
	if info.IsDir() {
		t.Error("Plugin path should be a file, not a directory")
	}

	// Verify file has content
	content, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("Failed to read plugin file: %v", err)
	}
	if len(content) == 0 {
		t.Error("Plugin file should have content")
	}
}

func TestEnsurePluginAt_CreatesDirectory(t *testing.T) {
	// Create a temporary directory
	tmpDir := t.TempDir()

	pluginDir := "nested/plugins/dir"
	pluginFile := "gastown.js"
	pluginPath := filepath.Join(tmpDir, pluginDir, pluginFile)

	// Create the plugin
	err := EnsurePluginAt(tmpDir, pluginDir, pluginFile)
	if err != nil {
		t.Fatalf("EnsurePluginAt() error = %v", err)
	}

	// Verify directory was created
	dirInfo, err := os.Stat(filepath.Dir(pluginPath))
	if err != nil {
		t.Fatalf("Plugin directory was not created: %v", err)
	}
	if !dirInfo.IsDir() {
		t.Error("Plugin parent path should be a directory")
	}
}

func TestEnsurePluginAt_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode checks are not reliable on Windows")
	}

	// Create a temporary directory
	tmpDir := t.TempDir()

	pluginDir := "plugins"
	pluginFile := "gastown.js"
	pluginPath := filepath.Join(tmpDir, pluginDir, pluginFile)

	err := EnsurePluginAt(tmpDir, pluginDir, pluginFile)
	if err != nil {
		t.Fatalf("EnsurePluginAt() error = %v", err)
	}

	info, err := os.Stat(pluginPath)
	if err != nil {
		t.Fatalf("Failed to stat plugin file: %v", err)
	}

	// Check file mode is 0644 (rw-r--r--)
	expectedMode := os.FileMode(0644)
	if info.Mode() != expectedMode {
		t.Errorf("Plugin file mode = %v, want %v", info.Mode(), expectedMode)
	}
}

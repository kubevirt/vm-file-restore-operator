/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name          string
		prepare       func(t *testing.T, dir string)
		wantDirSet    bool
		wantErrSubstr string
	}{
		{
			name: "project dir with go.mod regular file",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/tmp\n"), 0o644); err != nil {
					t.Fatalf("write go.mod: %v", err)
				}
			},
			wantDirSet: true,
		},
		{
			name: "standalone dir without go.mod",
			prepare: func(t *testing.T, _ string) {
				t.Helper()
			},
			wantDirSet: false,
		},
		{
			name: "go.mod exists but is not a regular file",
			prepare: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "go.mod"), 0o755); err != nil {
					t.Fatalf("mkdir go.mod: %v", err)
				}
			},
			wantErrSubstr: "not a regular file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: Run uses Getwd/Chdir semantics process-wide via t.Chdir.
			dir := t.TempDir()
			tt.prepare(t, dir)
			t.Chdir(dir)

			cmd := exec.Command("echo", "ok")
			out, err := Run(cmd)

			if tt.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (out=%q)", tt.wantErrSubstr, out)
				}
				if !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErrSubstr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if strings.TrimSpace(out) != "ok" {
				t.Fatalf("unexpected output %q", out)
			}
			if tt.wantDirSet {
				if cmd.Dir != dir {
					t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, dir)
				}
			} else if cmd.Dir != "" {
				t.Fatalf("cmd.Dir = %q, want empty for standalone mode", cmd.Dir)
			}
		})
	}
}

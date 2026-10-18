// Vikunja is a to-do list application to facilitate your life.
// Copyright 2018-present Vikunja and contributors. All rights reserved.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"code.vikunja.io/api/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The restore command exits the process on failure, so the test runs it in a child process.
func TestRestoreCmdExitsNonZeroOnFailure(t *testing.T) {
	if os.Getenv("VIKUNJA_TEST_RESTORE_CMD_CHILD") == "1" {
		log.InitLogger()
		restoreCmd.Run(restoreCmd, []string{filepath.Join(t.TempDir(), "missing.zip")})
		return
	}

	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRestoreCmdExitsNonZeroOnFailure$") //nolint:gosec // re-runs this test binary
	child.Env = append(os.Environ(), "VIKUNJA_TEST_RESTORE_CMD_CHILD=1")
	err := child.Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.ExitCode())
}

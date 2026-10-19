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
	"bytes"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"code.vikunja.io/api/pkg/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// openPty returns the controlling and the terminal side of a new pseudo-terminal.
func openPty(t *testing.T) (ptm, pts *os.File) {
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ptm.Close() })
	require.NoError(t, unix.IoctlSetPointerInt(int(ptm.Fd()), unix.TIOCSPTLCK, 0))
	n, err := unix.IoctlGetUint32(int(ptm.Fd()), unix.TIOCGPTN)
	require.NoError(t, err)
	pts, err = os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|unix.O_NOCTTY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pts.Close() })
	return ptm, pts
}

// The password prompt reads from a terminal and exits the process on failure,
// so each case runs in a child process with a pseudo-terminal as stdin.
func TestGetPasswordFromFlagOrInput(t *testing.T) {
	if os.Getenv("VIKUNJA_TEST_PASSWORD_CHILD") != "" {
		log.InitLogger()
		getPasswordFromFlagOrInput()
		return
	}

	cases := map[string]struct {
		input    string
		exitCode int
	}{
		"matching passwords":   {input: "secret1\nsecret1\n", exitCode: 0},
		"mismatched passwords": {input: "secret1\nsecret2\n", exitCode: 1},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ptm, pts := openPty(t)
			var out bytes.Buffer
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestGetPasswordFromFlagOrInput$") //nolint:gosec // re-runs this test binary
			child.Env = append(os.Environ(), "VIKUNJA_TEST_PASSWORD_CHILD=1")
			child.Stdin = pts
			child.Stdout = &out
			child.Stderr = &out
			_, err := io.WriteString(ptm, tc.input)
			require.NoError(t, err)
			go func() { _, _ = io.Copy(io.Discard, ptm) }() // drain the terminal echo

			err = child.Run()

			if tc.exitCode == 0 {
				require.NoError(t, err, out.String())
				return
			}
			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr)
			assert.Equal(t, tc.exitCode, exitErr.ExitCode())
			assert.Contains(t, out.String(), "Passwords don't match!")
		})
	}
}

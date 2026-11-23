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

package webtests

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.vikunja.io/api/pkg/modules/auth"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskByIdentifier(t *testing.T) {
	e, err := setupTestEnv()
	require.NoError(t, err)

	token, err := auth.NewUserJWTAuthtoken(&testuser1, "test-session-id")
	require.NoError(t, err)

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// Task 1 is the first task of project 1, whose identifier is "TEST1".
	for _, path := range []string{
		"/api/v1/tasks/TEST1-1",
		"/api/v1/tasks/test1-1",
		"/api/v2/tasks/TEST1-1",
	} {
		t.Run(path, func(t *testing.T) {
			rec := do(path)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), `"id":1,`)
		})
	}

	t.Run("sub-resource", func(t *testing.T) {
		rec := do("/api/v1/tasks/TEST1-1/comments")
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("unknown index", func(t *testing.T) {
		rec := do("/api/v1/tasks/TEST1-999999")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unknown project", func(t *testing.T) {
		rec := do("/api/v1/tasks/NOPE-1")
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("task in a project the user can't see", func(t *testing.T) {
		// Project 2 (TEST2) belongs to user 3.
		rec := do("/api/v1/tasks/TEST2-1")
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})
}

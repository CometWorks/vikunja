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

package db

import (
	"encoding/json"
	"testing"
	"time"

	"code.vikunja.io/api/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDumpTime(t *testing.T) {
	t.Run("rfc3339 utc", func(t *testing.T) {
		parsed, err := parseDumpTime("2026-03-27T11:27:01Z")
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 3, 27, 11, 27, 1, 0, time.UTC), parsed.UTC())
	})

	t.Run("rfc3339 with offset", func(t *testing.T) {
		parsed, err := parseDumpTime("2024-02-06T08:32:51+01:00")
		require.NoError(t, err)
		assert.Equal(t, time.Date(2024, 2, 6, 7, 32, 51, 0, time.UTC), parsed.UTC())
	})

	t.Run("rfc3339 with nanoseconds", func(t *testing.T) {
		parsed, err := parseDumpTime("2026-03-27T11:27:01.123456789Z")
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 3, 27, 11, 27, 1, 123456789, time.UTC), parsed.UTC())
	})

	t.Run("space separated without timezone", func(t *testing.T) {
		parsed, err := parseDumpTime("2026-03-27 11:27:01")
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 3, 27, 11, 27, 1, 0, time.UTC), parsed.UTC())
	})

	t.Run("t separated without timezone", func(t *testing.T) {
		parsed, err := parseDumpTime("2026-03-27T11:27:01")
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 3, 27, 11, 27, 1, 0, time.UTC), parsed.UTC())
	})

	t.Run("date only", func(t *testing.T) {
		parsed, err := parseDumpTime("2026-03-27")
		require.NoError(t, err)
		assert.Equal(t, time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC), parsed.UTC())
	})

	t.Run("invalid", func(t *testing.T) {
		_, err := parseDumpTime("not-a-date")
		require.Error(t, err)
	})
}

type dumpRestoreTest struct {
	ID       int64          `xorm:"autoincr not null unique pk"`
	Title    string         `xorm:"varchar(250)"`
	Created  time.Time      `xorm:"not null"`
	Updated  time.Time      `xorm:""`
	Done     bool           `xorm:"bool default false"`
	Position float64        `xorm:"double null"`
	Settings map[string]any `xorm:"json null"`
	ParentID *int64         `xorm:"bigint null"`
	Token    *string        `xorm:"varchar(64) null"`
}

func TestRestore(t *testing.T) {
	config.InitDefaultConfig()
	engine, err := CreateTestEngine()
	require.NoError(t, err)
	require.NoError(t, engine.Sync(&dumpRestoreTest{}))

	t.Run("time values from a dump", func(t *testing.T) {
		err := RestoreAndTruncate("dump_restore_test", []map[string]interface{}{
			{
				"id":      int64(1),
				"title":   "test",
				"created": "2026-03-27T11:27:01Z",
				"updated": "0001-01-01T00:00:00Z",
			},
		})
		require.NoError(t, err)

		row := &dumpRestoreTest{ID: 1}
		has, err := engine.Get(row)
		require.NoError(t, err)
		require.True(t, has)
		assert.Equal(t, time.Date(2026, 3, 27, 11, 27, 1, 0, time.UTC).Unix(), row.Created.Unix())
		assert.True(t, row.Updated.IsZero())
	})

	t.Run("unknown columns are dropped", func(t *testing.T) {
		err := RestoreAndTruncate("dump_restore_test", []map[string]interface{}{
			{
				"id":             int64(2),
				"title":          "test2",
				"created":        "2026-03-27 11:27:01",
				"does_not_exist": "some value",
			},
		})
		require.NoError(t, err)

		row := &dumpRestoreTest{ID: 2}
		has, err := engine.Get(row)
		require.NoError(t, err)
		require.True(t, has)
		assert.Equal(t, "test2", row.Title)
	})

	t.Run("numeric bool values from a sqlite dump", func(t *testing.T) {
		// json.Unmarshal decodes numbers as float64
		err := RestoreAndTruncate("dump_restore_test", []map[string]interface{}{
			{"id": int64(4), "created": "2026-03-27 11:27:01", "done": float64(1)},
			{"id": int64(5), "created": "2026-03-27 11:27:01", "done": float64(0)},
		})
		require.NoError(t, err)

		done := &dumpRestoreTest{ID: 4}
		has, err := engine.Get(done)
		require.NoError(t, err)
		require.True(t, has)
		assert.True(t, done.Done)

		notDone := &dumpRestoreTest{ID: 5}
		has, err = engine.Get(notDone)
		require.NoError(t, err)
		require.True(t, has)
		assert.False(t, notDone.Done)
	})

	t.Run("restore a dump of the table", func(t *testing.T) {
		_, err := engine.Where("1 = 1").Delete(&dumpRestoreTest{})
		require.NoError(t, err)
		created := time.Date(2026, 3, 27, 11, 27, 1, 0, time.UTC)
		parentID, zeroID := int64(5), int64(0)
		token, emptyToken := "abc", ""
		_, err = engine.Insert([]*dumpRestoreTest{
			{
				ID:       6,
				Title:    "with settings",
				Created:  created,
				Position: 1.5,
				Settings: map[string]any{"theme": "dark"},
				ParentID: &parentID,
				Token:    &token,
			},
			{
				ID:      7,
				Title:   "without settings",
				Created: created,
			},
			{
				ID:       8,
				Title:    "zero values",
				Created:  created,
				ParentID: &zeroID,
				Token:    &emptyToken,
			},
		})
		require.NoError(t, err)

		metas, err := tableMetas()
		require.NoError(t, err)
		dumped, err := dumpTable("dump_restore_test", metas["dump_restore_test"])
		require.NoError(t, err)

		// Restore reads the dump back the same way
		contents := []map[string]interface{}{}
		require.NoError(t, json.Unmarshal(dumped, &contents))
		require.NoError(t, RestoreAndTruncate("dump_restore_test", contents))

		restored := []*dumpRestoreTest{}
		require.NoError(t, engine.OrderBy("id").Find(&restored))
		require.Len(t, restored, 3)
		assert.InDelta(t, 1.5, restored[0].Position, 0)
		assert.Equal(t, map[string]any{"theme": "dark"}, restored[0].Settings)
		assert.Equal(t, created.Unix(), restored[0].Created.Unix())
		assert.Equal(t, &parentID, restored[0].ParentID)
		assert.Equal(t, &token, restored[0].Token)
		assert.InDelta(t, 0, restored[1].Position, 0)
		assert.Nil(t, restored[1].Settings)
		assert.Nil(t, restored[1].ParentID)
		assert.Nil(t, restored[1].Token)
		assert.Equal(t, &zeroID, restored[2].ParentID)
		assert.Equal(t, &emptyToken, restored[2].Token)
	})

	t.Run("invalid time value", func(t *testing.T) {
		err := RestoreAndTruncate("dump_restore_test", []map[string]interface{}{
			{
				"id":      int64(3),
				"created": "definitely-not-a-date",
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not parse time value")
	})
}

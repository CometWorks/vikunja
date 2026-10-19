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

package websocket

import (
	"testing"

	"code.vikunja.io/api/pkg/events"
	"code.vikunja.io/api/pkg/models"
	"code.vikunja.io/api/pkg/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func taskChangeConn(userID int64, subscriptions ...string) *Connection {
	subs := map[string]bool{}
	for _, s := range subscriptions {
		subs[s] = true
	}
	return &Connection{userID: userID, subscriptions: subs, send: make(chan OutgoingMessage, 16)}
}

func TestTaskChangeListener(t *testing.T) {
	// Fixtures: project 1 belongs to user 1, user 2 cannot read it.
	t.Run("pushes the ids of a changed task to its readers", func(t *testing.T) {
		s := setupDBTest(t)
		canRead, _, err := (&models.Project{ID: 1}).CanRead(s, &user.User{ID: 2})
		require.NoError(t, err)
		require.False(t, canRead, "fixture assumption: user 2 cannot read project 1")

		InitHub()
		reader := taskChangeConn(1, "task.changed")
		stranger := taskChangeConn(2, "task.changed")
		GetHub().Register(reader)
		GetHub().Register(stranger)

		ev := &models.TaskUpdatedEvent{Task: &models.Task{ID: 1, ProjectID: 1}, Doer: &user.User{ID: 1}}
		events.TestListener(t, ev, &TaskChangeListener{wsEvent: "task.changed"})

		require.Len(t, reader.send, 1)
		msg := <-reader.send
		assert.Equal(t, "task.changed", msg.Event)
		assert.Equal(t, &TaskChange{TaskID: 1, ProjectID: 1, DoerID: 1}, msg.Data)
		assert.Empty(t, stranger.send, "users who cannot read the project get nothing")
	})

	t.Run("loads the project when the event lacks it", func(t *testing.T) {
		setupDBTest(t)
		InitHub()
		reader := taskChangeConn(1, "task.changed")
		GetHub().Register(reader)

		ev := &models.TaskAssigneeCreatedEvent{Task: &models.Task{ID: 1}}
		events.TestListener(t, ev, &TaskChangeListener{wsEvent: "task.changed"})

		require.Len(t, reader.send, 1)
		assert.Equal(t, int64(1), (<-reader.send).Data.(*TaskChange).ProjectID)
	})

	t.Run("comment events carry the comment id", func(t *testing.T) {
		setupDBTest(t)
		InitHub()
		commentReader := taskChangeConn(1, "comment.changed")
		taskOnly := taskChangeConn(1, "task.changed")
		GetHub().Register(commentReader)
		GetHub().Register(taskOnly)

		ev := &models.TaskCommentCreatedEvent{
			Task:    &models.Task{ID: 1, ProjectID: 1},
			Comment: &models.TaskComment{ID: 5},
		}
		events.TestListener(t, ev, &TaskChangeListener{wsEvent: "comment.changed"})

		require.Len(t, commentReader.send, 1)
		msg := <-commentReader.send
		assert.Equal(t, "comment.changed", msg.Event)
		assert.Equal(t, &TaskChange{TaskID: 1, ProjectID: 1, CommentID: 5}, msg.Data)
		assert.Empty(t, taskOnly.send, "only subscribers of the event get it")
	})
}

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
	"encoding/json"

	"code.vikunja.io/api/pkg/db"
	"code.vikunja.io/api/pkg/events"
	"code.vikunja.io/api/pkg/license"
	"code.vikunja.io/api/pkg/log"
	"code.vikunja.io/api/pkg/models"
	"code.vikunja.io/api/pkg/notifications"
	"code.vikunja.io/api/pkg/user"

	"github.com/ThreeDotsLabs/watermill/message"
)

// NotificationListener pushes new notifications to WebSocket clients.
type NotificationListener struct{}

// Name returns the listener name.
func (n *NotificationListener) Name() string {
	return "websocket.notification.push"
}

// Handle processes a notification created event, reloads the notification
// from the database (to get accurate timestamps), and pushes it to the
// relevant WebSocket connections.
func (n *NotificationListener) Handle(msg *message.Message) error {
	var event notifications.NotificationCreatedEvent
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return err
	}

	hub := GetHub()
	if hub == nil {
		log.Warningf("WebSocket: hub not initialized, skipping notification push")
		return nil
	}

	s := db.NewSession()
	defer s.Close()

	dbNotification, err := notifications.GetNotificationByID(s, event.NotificationID)
	if err != nil {
		log.Errorf("WebSocket: failed to load notification %d: %v", event.NotificationID, err)
		return nil
	}
	if dbNotification == nil {
		log.Warningf("WebSocket: notification %d not found, skipping push", event.NotificationID)
		return nil
	}

	canRead, err := models.CanReadNotification(s, &user.User{ID: event.UserID}, dbNotification)
	if err != nil {
		log.Errorf("WebSocket: failed to check access for notification %d: %v", event.NotificationID, err)
		return nil
	}
	if !canRead {
		return nil
	}

	hub.PublishForUser(event.UserID, "notification.created", dbNotification)
	return nil
}

// TimeEntryListener pushes a user's own timer changes to their WebSocket
// connections. wsEvent is "timer.created", "timer.updated" or "timer.deleted";
// the payload is the full entry, so the running-elsewhere badge reads end_time
// to know whether a timer is active (and the id to drop a deleted one). Not
// emitted on unlicensed instances.
type TimeEntryListener struct {
	wsEvent string
}

func (l *TimeEntryListener) Name() string { return "websocket.push." + l.wsEvent }

func (l *TimeEntryListener) Handle(msg *message.Message) error {
	if !license.IsFeatureEnabled(license.FeatureTimeTracking) {
		return nil
	}

	// All TimeEntry events share the {time_entry, doer} shape; only the entry is needed.
	var event struct {
		TimeEntry *models.TimeEntry `json:"time_entry"`
	}
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return err
	}
	if event.TimeEntry == nil {
		return nil
	}

	hub := GetHub()
	if hub == nil {
		log.Warningf("WebSocket: hub not initialized, skipping timer push")
		return nil
	}

	hub.PublishForUser(event.TimeEntry.UserID, l.wsEvent, event.TimeEntry)
	return nil
}

// TaskChange is the payload of the task.changed and comment.changed events. It
// only holds ids: clients refetch through the API, which applies their own
// permissions and formats.
type TaskChange struct {
	TaskID    int64 `json:"task_id"`
	ProjectID int64 `json:"project_id"`
	CommentID int64 `json:"comment_id,omitempty"`
	DoerID    int64 `json:"doer_id,omitempty"`
}

// TaskChangeListener tells every connected user who can read a task that the
// task or one of its comments changed, so their open lists, boards and task
// details refresh.
//
// ponytail: one read check per connected user and event, fine for team-sized
// instances. Cache project access per user if that gets slow.
type TaskChangeListener struct {
	wsEvent string
}

func (l *TaskChangeListener) Name() string { return "websocket.push." + l.wsEvent }

func (l *TaskChangeListener) Handle(msg *message.Message) error {
	// Every task event carries the task, comment events also the comment.
	var event struct {
		Task    *models.Task        `json:"task"`
		Comment *models.TaskComment `json:"comment"`
		Doer    *user.User          `json:"doer"`
	}
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return err
	}
	if event.Task == nil || event.Task.ID == 0 {
		return nil
	}

	hub := GetHub()
	if hub == nil {
		return nil
	}
	userIDs := hub.SubscribedUserIDs(l.wsEvent)
	if len(userIDs) == 0 {
		return nil
	}

	s := db.NewSession()
	defer s.Close()

	change := &TaskChange{TaskID: event.Task.ID, ProjectID: event.Task.ProjectID}
	if change.ProjectID == 0 {
		task, err := models.GetTaskByIDSimple(s, change.TaskID)
		if err != nil {
			log.Errorf("WebSocket: failed to load task %d: %v", change.TaskID, err)
			return nil
		}
		change.ProjectID = task.ProjectID
	}
	if event.Comment != nil {
		change.CommentID = event.Comment.ID
	}
	if event.Doer != nil {
		change.DoerID = event.Doer.ID
	}

	project := &models.Project{ID: change.ProjectID}
	for _, userID := range userIDs {
		canRead, _, err := project.CanRead(s, &user.User{ID: userID})
		if err != nil {
			log.Errorf("WebSocket: failed to check access to project %d for user %d: %v", change.ProjectID, userID, err)
			continue
		}
		if canRead {
			hub.PublishForUser(userID, l.wsEvent, change)
		}
	}
	return nil
}

type SessionsRevokedListener struct{}

// Name defines the name for the SessionsRevokedListener listener
func (l *SessionsRevokedListener) Name() string { return "websocket.sessions.revoked" }

// Handle is executed when the event SessionsRevokedListener listens on is fired
func (l *SessionsRevokedListener) Handle(msg *message.Message) error {
	var event models.SessionsRevokedEvent
	if err := json.Unmarshal(msg.Payload, &event); err != nil {
		return err
	}
	hub := GetHub()
	if hub == nil {
		return nil
	}
	if event.SessionID == "" {
		hub.DisconnectUser(event.UserID)
		return nil
	}
	hub.DisconnectSession(event.UserID, event.SessionID)
	return nil
}

// RegisterListeners registers WebSocket event listeners.
func RegisterListeners() {
	events.RegisterListener(
		(&notifications.NotificationCreatedEvent{}).Name(),
		&NotificationListener{},
	)
	events.RegisterListener((&models.TimeEntryCreatedEvent{}).Name(), &TimeEntryListener{wsEvent: "timer.created"})
	events.RegisterListener((&models.TimeEntryUpdatedEvent{}).Name(), &TimeEntryListener{wsEvent: "timer.updated"})
	events.RegisterListener((&models.TimeEntryDeletedEvent{}).Name(), &TimeEntryListener{wsEvent: "timer.deleted"})
	events.RegisterListener((&models.SessionsRevokedEvent{}).Name(), &SessionsRevokedListener{})

	for _, event := range []events.Event{
		&models.TaskCreatedEvent{},
		&models.TaskUpdatedEvent{},
		&models.TaskDeletedEvent{},
		&models.TaskAssigneeCreatedEvent{},
		&models.TaskAssigneeDeletedEvent{},
		&models.TaskAttachmentCreatedEvent{},
		&models.TaskAttachmentDeletedEvent{},
		&models.TaskRelationCreatedEvent{},
		&models.TaskRelationDeletedEvent{},
	} {
		events.RegisterListener(event.Name(), &TaskChangeListener{wsEvent: "task.changed"})
	}
	for _, event := range []events.Event{
		&models.TaskCommentCreatedEvent{},
		&models.TaskCommentUpdatedEvent{},
		&models.TaskCommentDeletedEvent{},
	} {
		events.RegisterListener(event.Name(), &TaskChangeListener{wsEvent: "comment.changed"})
	}
}

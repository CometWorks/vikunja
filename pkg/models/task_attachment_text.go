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

package models

import (
	"bytes"
	"io"
	"sync"
	"unicode/utf8"

	"code.vikunja.io/api/pkg/cron"
	"code.vikunja.io/api/pkg/db"
	"code.vikunja.io/api/pkg/files"
	"code.vikunja.io/api/pkg/log"

	"xorm.io/builder"
	"xorm.io/xorm"
)

// maxAttachmentTextSize caps how much of an attachment is indexed for search.
const maxAttachmentTextSize = 1 << 20

// TaskAttachmentText holds the searchable text of an attachment. Attachments
// that are not plain text get an empty row, so they are not read again.
type TaskAttachmentText struct {
	AttachmentID int64  `xorm:"bigint not null unique pk"`
	TaskID       int64  `xorm:"bigint not null index"`
	Content      string `xorm:"longtext not null"`
}

func (*TaskAttachmentText) TableName() string { return "task_attachment_texts" }

// extractAttachmentText returns the start of r as a string when it is plain
// UTF-8 text, and "" for anything else. Binary files and compressed formats
// (zip, gzip, pdf, docx, images) contain invalid UTF-8 or NUL bytes, so they
// are left out.
func extractAttachmentText(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxAttachmentTextSize))
	if err != nil {
		return "", err
	}
	if len(data) == maxAttachmentTextSize {
		// The cap can cut a multi-byte character in half.
		for i := 0; i < utf8.UTFMax-1 && !utf8.Valid(data); i++ {
			data = data[:len(data)-1]
		}
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", nil
	}
	return string(data), nil
}

func saveAttachmentText(s *xorm.Session, ta *TaskAttachment, r io.Reader) error {
	text, err := extractAttachmentText(r)
	if err != nil {
		return err
	}
	_, err = s.Insert(&TaskAttachmentText{AttachmentID: ta.ID, TaskID: ta.TaskID, Content: text})
	return err
}

// RegisterAttachmentTextIndexCron indexes attachments that have no text row yet:
// the ones uploaded before attachment search existed, and any written without
// NewAttachment.
func RegisterAttachmentTextIndexCron() {
	err := cron.Schedule("* * * * *", indexAttachmentTexts)
	if err != nil {
		log.Errorf("Could not register attachment text index cron: %s", err.Error())
	}
}

var indexAttachmentTextsRunning sync.Mutex

func indexAttachmentTexts() {
	// A long backfill can outlast the one minute interval.
	if !indexAttachmentTextsRunning.TryLock() {
		return
	}
	defer indexAttachmentTextsRunning.Unlock()

	for {
		s := db.NewSession()
		attachments := []*TaskAttachment{}
		err := s.
			Where(builder.NotIn("id", builder.Select("attachment_id").From("task_attachment_texts"))).
			OrderBy("id").
			Limit(100).
			Find(&attachments)
		s.Close()
		if err != nil {
			log.Errorf("Could not get attachments to index: %s", err)
			return
		}

		for _, ta := range attachments {
			if err := indexAttachmentText(ta); err != nil {
				// Stop instead of skipping, otherwise the same batch comes back forever.
				log.Errorf("Could not index the text of attachment %d: %s", ta.ID, err)
				return
			}
		}

		if len(attachments) < 100 {
			return
		}
	}
}

func indexAttachmentText(ta *TaskAttachment) error {
	file := &files.File{ID: ta.FileID}
	var content io.Reader = &bytes.Reader{}
	err := file.LoadFileByID()
	switch {
	case files.IsErrFileDoesNotExist(err):
		// Nothing to index, the empty row keeps it from being tried again.
	case err != nil:
		return err
	default:
		defer file.File.Close()
		content = file.File
	}

	s := db.NewSession()
	defer s.Close()
	if err := saveAttachmentText(s, ta, content); err != nil {
		_ = s.Rollback()
		return err
	}
	return s.Commit()
}

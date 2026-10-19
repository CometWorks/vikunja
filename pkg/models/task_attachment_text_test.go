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
	"compress/gzip"
	"strings"
	"testing"

	"code.vikunja.io/api/pkg/db"
	"code.vikunja.io/api/pkg/files"
	"code.vikunja.io/api/pkg/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractAttachmentText(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, err := w.Write([]byte("compressed text"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// "é" is two bytes; placed so the size cap cuts it in half.
	cutRune := strings.Repeat("a", maxAttachmentTextSize-1) + "é"

	tests := []struct {
		name    string
		content []byte
		want    string
	}{
		{"plain text", []byte("meeting notes\nline two"), "meeting notes\nline two"},
		{"utf-8 text", []byte("árvíztűrő tükörfúrógép"), "árvíztűrő tükörfúrógép"},
		{"gzip is not indexed", gz.Bytes(), ""},
		{"nul bytes are not indexed", []byte("abc\x00def"), ""},
		{"invalid utf-8 is not indexed", []byte{0xff, 0xfe, 'a'}, ""},
		{"cap cutting a character keeps the text", []byte(cutRune), strings.Repeat("a", maxAttachmentTextSize-1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractAttachmentText(bytes.NewReader(tt.content))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTaskSearchAttachments(t *testing.T) {
	db.LoadAndAssertFixtures(t)
	files.InitTestFileFixtures(t)
	s := db.NewSession()
	defer s.Close()

	usr := &user.User{ID: 1}

	withText := &Task{Title: "supplier contract", ProjectID: 1}
	require.NoError(t, withText.Create(s, usr))
	content := []byte("Delivery of the capybara feed is due monthly.")
	ta := &TaskAttachment{TaskID: withText.ID}
	require.NoError(t, ta.NewAttachment(s, bytes.NewReader(content), "contract.txt", uint64(len(content)), usr))

	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, err := w.Write([]byte("capybara archive"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	compressed := &Task{Title: "archive upload", ProjectID: 1}
	require.NoError(t, compressed.Create(s, usr))
	gzAttachment := &TaskAttachment{TaskID: compressed.ID}
	require.NoError(t, gzAttachment.NewAttachment(s, bytes.NewReader(gz.Bytes()), "archive.gz", uint64(gz.Len()), usr))

	t.Run("text attachment content is found", func(t *testing.T) {
		pos := searchTaskPositions(t, s, usr, &TaskCollection{ProjectID: 1}, "capybara")
		assert.Contains(t, pos, withText.ID)
		assert.NotContains(t, pos, compressed.ID, "compressed attachments are not indexed")
	})

	t.Run("deleting the attachment removes its text", func(t *testing.T) {
		require.NoError(t, ta.Delete(s, usr))
		pos := searchTaskPositions(t, s, usr, &TaskCollection{ProjectID: 1}, "capybara")
		assert.NotContains(t, pos, withText.ID)
	})
}

func TestIndexAttachmentTexts(t *testing.T) {
	db.LoadAndAssertFixtures(t)
	files.InitTestFileFixtures(t)

	indexAttachmentTexts()

	s := db.NewSession()
	defer s.Close()

	attachments, err := s.Count(&TaskAttachment{})
	require.NoError(t, err)
	texts := []*TaskAttachmentText{}
	require.NoError(t, s.OrderBy("attachment_id").Find(&texts))
	assert.Len(t, texts, int(attachments), "every attachment gets a row, even without a readable file")

	byID := map[int64]string{}
	for _, text := range texts {
		byID[text.AttachmentID] = text.Content
	}
	assert.Equal(t, "testfile1", byID[1], "the fixture file holds plain text")
	assert.Empty(t, byID[2], "the file of attachment 2 does not exist")

	pos := searchTaskPositions(t, s, &user.User{ID: 1}, &TaskCollection{ProjectID: 1}, "testfile1")
	assert.Contains(t, pos, int64(1), "a backfilled attachment makes its task searchable")

	// A second run has nothing left to do.
	indexAttachmentTexts()
	count, err := s.Count(&TaskAttachmentText{})
	require.NoError(t, err)
	assert.Equal(t, attachments, count)
}

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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode"
	"unicode/utf8"

	"code.vikunja.io/api/pkg/config"
	"code.vikunja.io/api/pkg/db"
	"code.vikunja.io/api/pkg/user"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/xorm/schemas"
)

func TestChunkText(t *testing.T) {
	assert.Equal(t, []string{"short text"}, chunkText("  short text ", 100, 10))
	assert.Empty(t, chunkText("   ", 100, 10))

	words := []string{}
	for i := range 200 {
		words = append(words, fmt.Sprintf("word%03d", i))
	}
	chunks := chunkText(strings.Join(words, " "), 100, 20)
	require.Greater(t, len(chunks), 1)
	for _, c := range chunks {
		assert.LessOrEqual(t, utf8.RuneCountInString(c), 100)
	}
	for _, w := range words {
		assert.True(t, slices.ContainsFunc(chunks, func(c string) bool {
			return slices.Contains(strings.Fields(c), w)
		}), "%s must appear whole in a chunk", w)
	}
	for i := 1; i < len(chunks); i++ {
		prev := strings.Fields(chunks[i-1])
		assert.Contains(t, chunks[i], prev[len(prev)-1], "consecutive chunks overlap")
	}

	// A word longer than a chunk is cut instead of stalling.
	assert.Len(t, chunkText(strings.Repeat("x", 250), 100, 20), 3)
}

func TestTaskEmbeddingChunks(t *testing.T) {
	chunks, err := taskEmbeddingChunks(
		&Task{Title: "Plan trip", Description: "<p>Book the <strong>train</strong></p>"},
		[]*TaskComment{{Comment: "<p>Window seat</p>"}},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"Plan trip\n\nBook the **train**\n\nWindow seat"}, chunks)

	chunks, err = taskEmbeddingChunks(&Task{Title: "Only a title"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"Only a title"}, chunks)
}

// fakeEmbedding maps words to a few fixed concepts, so "car" and "automobile"
// get the same vector while sharing no text.
func fakeEmbedding(text string) []float32 {
	concepts := map[string]int{
		"car": 1, "automobile": 1, "vehicle": 1,
		"pizza": 2, "pasta": 2,
		"invoice": 3, "payment": 3,
	}
	v := []float32{0.05, 0, 0, 0}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if c, ok := concepts[w]; ok {
			v[c]++
		}
	}
	return v
}

// useFakeEmbeddingAPI turns semantic search on against a fake OpenAI-compatible
// API and returns its request counter.
func useFakeEmbeddingAPI(t *testing.T) *atomic.Int32 {
	t.Helper()
	if db.Type() != schemas.POSTGRES {
		t.Skip("semantic search needs PostgreSQL with pgvector")
	}

	requests := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "/v1/embeddings", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		assert.Equal(t, "fake-model", req.Model)
		data := []map[string]any{}
		for i, in := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": fakeEmbedding(in)})
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	t.Cleanup(srv.Close)

	config.SemanticSearchEnabled.Set(true)
	config.SemanticSearchURL.Set(srv.URL + "/v1/")
	config.SemanticSearchAPIKey.Set("test-key")
	config.SemanticSearchModel.Set("fake-model")
	config.SemanticSearchMaxDistance.Set(0.3)
	t.Cleanup(func() {
		config.SemanticSearchEnabled.Set(false)
		config.SemanticSearchAPIKey.Set("")
	})

	require.NoError(t, InitSemanticSearch())
	_, err := x.Exec("DELETE FROM task_embeddings")
	require.NoError(t, err)

	// Migrations, dumps and the doctor read every table's schema through xorm,
	// which fails on column types it does not know, like pgvector's vector.
	_, err = x.DBMetas()
	require.NoError(t, err)
	return requests
}

func TestSemanticSearch(t *testing.T) {
	db.LoadAndAssertFixtures(t)
	requests := useFakeEmbeddingAPI(t)
	usr := &user.User{ID: 1}

	s := db.NewSession()
	automobile := &Task{Title: "Buy a new automobile", ProjectID: 1}
	require.NoError(t, automobile.Create(s, usr))
	pizza := &Task{Title: "Order pizza for the team", ProjectID: 1}
	require.NoError(t, pizza.Create(s, usr))
	sync := &Task{Title: "Weekly sync", ProjectID: 1}
	require.NoError(t, sync.Create(s, usr))
	_, err := s.Insert(&TaskComment{Comment: "<p>We still owe them the invoice</p>", AuthorID: usr.ID, TaskID: sync.ID})
	require.NoError(t, err)
	// Task 13 lives in project 2, which user 1 cannot read.
	_, err = s.ID(13).Cols("title").Update(&Task{Title: "automobile repair"})
	require.NoError(t, err)
	require.NoError(t, s.Commit())
	s.Close()

	indexTaskEmbeddings()

	search := func(query string) map[int64]int {
		s := db.NewSession()
		defer s.Close()
		return searchTaskPositions(t, s, usr, &TaskCollection{}, query)
	}
	bumpUpdated := func(taskID int64) {
		_, err := x.Exec("UPDATE tasks SET updated = updated + interval '1 minute' WHERE id = ?", taskID)
		require.NoError(t, err)
	}

	t.Run("matches by meaning", func(t *testing.T) {
		pos := search("car")
		assert.Contains(t, pos, automobile.ID)
		assert.NotContains(t, pos, pizza.ID)
		assert.NotContains(t, pos, int64(13), "tasks the user cannot read stay hidden")
	})

	t.Run("comments are vectorized", func(t *testing.T) {
		assert.Contains(t, search("payment"), sync.ID)
	})

	t.Run("unchanged text is not vectorized again", func(t *testing.T) {
		bumpUpdated(pizza.ID)
		before := requests.Load()
		indexTaskEmbeddings()
		indexTaskEmbeddings()
		assert.Equal(t, before, requests.Load())
	})

	t.Run("changed text is vectorized again", func(t *testing.T) {
		_, err := x.ID(automobile.ID).Cols("title").Update(&Task{Title: "Sell the old pasta maker"})
		require.NoError(t, err)
		bumpUpdated(automobile.ID)
		before := requests.Load()
		indexTaskEmbeddings()
		assert.Equal(t, before+1, requests.Load())

		assert.NotContains(t, search("car"), automobile.ID)
		assert.Contains(t, search("pizza"), automobile.ID, "pasta is close to pizza")
	})

	t.Run("hard delete removes the vectors", func(t *testing.T) {
		s := db.NewSession()
		defer s.Close()
		require.NoError(t, hardDeleteTask(s, pizza))
		require.NoError(t, s.Commit())
		count, err := x.Table("task_embeddings").Where("task_id = ?", pizza.ID).Count()
		require.NoError(t, err)
		assert.Zero(t, count)
	})

	t.Run("text search still works when the API is down", func(t *testing.T) {
		config.SemanticSearchURL.Set("http://127.0.0.1:1")
		assert.Contains(t, search("Weekly"), sync.ID)
	})
}

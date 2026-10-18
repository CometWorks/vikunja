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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"code.vikunja.io/api/pkg/config"
	"code.vikunja.io/api/pkg/cron"
	"code.vikunja.io/api/pkg/db"
	"code.vikunja.io/api/pkg/log"
	"code.vikunja.io/api/pkg/richtext"

	"xorm.io/builder"
	"xorm.io/xorm"
	"xorm.io/xorm/schemas"
)

// Semantic search splits the title, description and comments of every task into
// overlapping chunks, vectorizes each chunk with an OpenAI-compatible embeddings
// API and stores the vectors in task_embeddings. A search vectorizes the query
// and also returns tasks with a chunk close to it, using pgvector's cosine distance.
//
// The vectors are stored as real[] and only cast to pgvector's vector type in the
// query. xorm cannot read the schema of a vector column, and migrations (Sync),
// dumps and the doctor all read the schema of every table.

const (
	embeddingChunkSize    = 2000 // characters, roughly 500 tokens
	embeddingChunkOverlap = 200
	maxEmbeddingChunks    = 100 // per task, bounds the cost of huge comment threads
	maxSemanticMatches    = 50
	embeddingBatchSize    = 50 // tasks per indexing query
	semanticSearchTimeout = 10 * time.Second
	embeddingIndexTimeout = 2 * time.Minute
)

func semanticSearchEnabled() bool {
	return config.SemanticSearchEnabled.GetBool()
}

// InitSemanticSearch creates the embeddings table when semantic search is enabled.
// It lives outside the migrations because it needs PostgreSQL with pgvector,
// which only installs that turn the feature on have.
func InitSemanticSearch() error {
	if !semanticSearchEnabled() {
		return nil
	}
	if db.Type() != schemas.POSTGRES {
		return fmt.Errorf("semantic search needs PostgreSQL with the pgvector extension")
	}

	for _, stmt := range []string{
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE TABLE IF NOT EXISTS task_embeddings (
			id BIGSERIAL PRIMARY KEY,
			task_id BIGINT NOT NULL,
			model VARCHAR(250) NOT NULL,
			task_updated TIMESTAMP NOT NULL,
			content_hash VARCHAR(64) NOT NULL,
			embedding REAL[] NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_embeddings_task_id ON task_embeddings (task_id)`,
	} {
		if _, err := x.Exec(stmt); err != nil {
			return fmt.Errorf("could not set up semantic search: %w", err)
		}
	}
	return nil
}

// chunkText splits text into chunks of at most size runes which overlap by about
// overlap runes. Chunks end at whitespace where the second half of a chunk has some.
func chunkText(text string, size, overlap int) []string {
	runes := []rune(strings.TrimSpace(text))
	chunks := []string{}
	for start := 0; start < len(runes); {
		end := min(start+size, len(runes))
		if end < len(runes) {
			for i := end; i > start+size/2; i-- {
				if unicode.IsSpace(runes[i]) {
					end = i
					break
				}
			}
		}
		if chunk := strings.TrimSpace(string(runes[start:end])); chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(runes) {
			break
		}
		start = max(end-overlap, start+1)
	}
	return chunks
}

// taskEmbeddingChunks returns the texts to vectorize for a task. Every chunk
// starts with the title, so a chunk from deep in a comment thread still says
// which task it belongs to.
func taskEmbeddingChunks(task *Task, comments []*TaskComment) ([]string, error) {
	parts := []string{}
	for _, html := range append([]string{task.Description}, commentTexts(comments)...) {
		text, err := richtext.HTMLToMarkdown(html)
		if err != nil {
			return nil, err
		}
		if text != "" {
			parts = append(parts, text)
		}
	}

	body := chunkText(strings.Join(parts, "\n\n"), embeddingChunkSize, embeddingChunkOverlap)
	if len(body) == 0 {
		return []string{task.Title}, nil
	}
	if len(body) > maxEmbeddingChunks {
		body = body[:maxEmbeddingChunks]
	}
	chunks := make([]string, len(body))
	for i, chunk := range body {
		chunks[i] = task.Title + "\n\n" + chunk
	}
	return chunks, nil
}

func commentTexts(comments []*TaskComment) []string {
	texts := make([]string, len(comments))
	for i, c := range comments {
		texts[i] = c.Comment
	}
	return texts
}

// embedTexts calls the OpenAI-compatible embeddings API and returns one vector per text.
func embedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{
		"model": config.SemanticSearchModel.GetString(),
		"input": texts,
	})
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(config.SemanticSearchURL.GetString(), "/") + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := config.SemanticSearchAPIKey.GetString(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("embeddings API returned %s: %s", resp.Status, msg)
	}

	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("could not decode the embeddings API response: %w", err)
	}

	vectors := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(texts) || len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings API returned an invalid item at index %d", d.Index)
		}
		vectors[d.Index] = d.Embedding
	}
	for i, v := range vectors {
		if v == nil {
			return nil, fmt.Errorf("embeddings API returned no vector for input %d", i)
		}
	}
	return vectors, nil
}

// vectorLiteral formats a vector as pgvector text input. It only holds numbers,
// so it is safe to put into SQL directly.
func vectorLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// findSemanticMatches returns the ids of tasks in scope with a chunk close in
// meaning to the search, closest first. When the embeddings API fails the search
// goes on as a plain text search.
func (d *dbTaskSearcher) findSemanticMatches(search string, scope builder.Cond) ([]int64, error) {
	if !semanticSearchEnabled() || search == "" || isTaskIndexSearch(search) {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), semanticSearchTimeout)
	defer cancel()
	vectors, err := embedTexts(ctx, []string{search})
	if err != nil {
		log.Errorf("Semantic search is unavailable, falling back to text search: %s", err)
		return nil, nil
	}

	distance := "(task_embeddings.embedding::vector <=> '" + vectorLiteral(vectors[0]) + "'::vector)"
	ids := []int64{}
	err = d.s.
		Table("task_embeddings").
		Join("INNER", "tasks", "tasks.id = task_embeddings.task_id").
		Where(builder.And(
			scope,
			taskNotDeletedCond("tasks"),
			builder.Eq{"task_embeddings.model": config.SemanticSearchModel.GetString()},
			builder.Expr(distance+" <= ?", config.SemanticSearchMaxDistance.GetFloat64()),
		)).
		GroupBy("task_embeddings.task_id").
		OrderBy("MIN(" + distance + ")").
		Limit(maxSemanticMatches).
		Cols("task_embeddings.task_id").
		Find(&ids)
	return ids, err
}

// RegisterTaskEmbeddingCron keeps the embeddings in sync with the tasks. A task
// needs new embeddings when it has none for the configured model, or when it was
// updated after they were made. Comment changes bump the task's updated time too.
func RegisterTaskEmbeddingCron() {
	if !semanticSearchEnabled() {
		return
	}
	err := cron.Schedule("* * * * *", indexTaskEmbeddings)
	if err != nil {
		log.Errorf("Could not register task embedding cron: %s", err.Error())
	}
}

var indexTaskEmbeddingsRunning sync.Mutex

func indexTaskEmbeddings() {
	// A long backfill can outlast the one minute interval.
	if !indexTaskEmbeddingsRunning.TryLock() {
		return
	}
	defer indexTaskEmbeddingsRunning.Unlock()

	model := config.SemanticSearchModel.GetString()
	for {
		stale := []struct {
			ID      int64
			Updated string
		}{}
		s := db.NewSession()
		// The updated time stays text so it goes back into the database unchanged.
		err := s.SQL(`SELECT tasks.id, tasks.updated::text AS updated FROM tasks
			WHERE tasks.deleted_at IS NULL AND NOT EXISTS (
				SELECT 1 FROM task_embeddings e
				WHERE e.task_id = tasks.id AND e.model = ? AND e.task_updated >= tasks.updated
			)
			ORDER BY tasks.id LIMIT ?`, model, embeddingBatchSize).
			Find(&stale)
		s.Close()
		if err != nil {
			log.Errorf("Could not get tasks to vectorize: %s", err)
			return
		}

		for _, t := range stale {
			if err := indexTaskEmbedding(t.ID, t.Updated, model); err != nil {
				// Stop instead of skipping, otherwise the same batch comes back forever.
				log.Errorf("Could not vectorize task %d: %s", t.ID, err)
				return
			}
		}

		if len(stale) < embeddingBatchSize {
			return
		}
	}
}

func indexTaskEmbedding(taskID int64, updated, model string) error {
	chunks, unchanged, contentHash, err := loadTaskEmbeddingChunks(taskID, model)
	if err != nil {
		return err
	}

	var vectors [][]float32
	if !unchanged {
		// No transaction is open while waiting for the API.
		ctx, cancel := context.WithTimeout(context.Background(), embeddingIndexTimeout)
		defer cancel()
		vectors, err = embedTexts(ctx, chunks)
		if err != nil {
			return err
		}
	}

	s := db.NewSession()
	defer s.Close()
	if err := saveTaskEmbeddings(s, taskID, updated, model, contentHash, unchanged, vectors); err != nil {
		_ = s.Rollback()
		return err
	}
	return s.Commit()
}

// loadTaskEmbeddingChunks returns the chunks of a task and whether the stored
// embeddings already cover exactly that text.
func loadTaskEmbeddingChunks(taskID int64, model string) (chunks []string, unchanged bool, contentHash string, err error) {
	s := db.NewSession()
	defer s.Close()

	task, err := GetTaskByIDSimple(s, taskID)
	if err != nil {
		return nil, false, "", err
	}
	comments := []*TaskComment{}
	if err := s.Where("task_id = ?", taskID).OrderBy("id").Find(&comments); err != nil {
		return nil, false, "", err
	}
	chunks, err = taskEmbeddingChunks(&task, comments)
	if err != nil {
		return nil, false, "", err
	}

	hash := sha256.Sum256([]byte(strings.Join(chunks, "\x00")))
	contentHash = hex.EncodeToString(hash[:])

	// Most updates (done, labels, positions) leave the text alone. Those only
	// need the new updated time, not another round trip to the API.
	unchanged, err = s.Table("task_embeddings").
		Where("task_id = ? AND model = ? AND content_hash = ?", taskID, model, contentHash).
		Exist()
	return chunks, unchanged, contentHash, err
}

func saveTaskEmbeddings(s *xorm.Session, taskID int64, updated, model, contentHash string, unchanged bool, vectors [][]float32) error {
	if unchanged {
		_, err := s.Exec("UPDATE task_embeddings SET task_updated = ?::timestamp WHERE task_id = ? AND model = ?", updated, taskID, model)
		return err
	}

	if _, err := s.Exec("DELETE FROM task_embeddings WHERE task_id = ?", taskID); err != nil {
		return err
	}
	for _, v := range vectors {
		_, err := s.Exec(`INSERT INTO task_embeddings (task_id, model, task_updated, content_hash, embedding)
			VALUES (?, ?, ?::timestamp, ?, ?::vector::real[])`, taskID, model, updated, contentHash, vectorLiteral(v))
		if err != nil {
			return err
		}
	}
	return nil
}

func deleteTaskEmbeddings(s *xorm.Session, taskID int64) error {
	if !semanticSearchEnabled() {
		return nil
	}
	_, err := s.Exec("DELETE FROM task_embeddings WHERE task_id = ?", taskID)
	return err
}

// Package milvus provides a Milvus v2 gRPC client wrapper for vector storage
// and similarity search. When Milvus is unavailable the caller (memory.Store)
// falls back to BM25-only search so this package is entirely opt-in.
package milvus

import (
	"context"
	"fmt"
	"strconv"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

const (
	defaultEmbedDim = 1536

	fieldID        = "id"
	fieldPeerID    = "peer_id"
	fieldContent   = "content"
	fieldTags      = "tags"
	fieldCategory  = "category"
	fieldEmbedding = "embedding"
)

// Client wraps the Milvus v2 gRPC client for memory chunk operations.
type Client struct {
	cli        *milvusclient.Client
	collection string
	embedDim   int
}

// QueryResult is a single result from a Milvus similarity search.
type QueryResult struct {
	ID       string
	Document string
	Score    float32 // cosine similarity score [higher = more similar]
	Metadata map[string]any
}

// New connects to a Milvus server and returns a ready-to-use client.
// addr is the gRPC address (e.g., "localhost:19530").
// embedDim is the embedding vector dimension; pass 0 to use the default (1536).
func New(ctx context.Context, addr, collectionName string, embedDim int) (*Client, error) {
	if embedDim <= 0 {
		embedDim = defaultEmbedDim
	}
	cli, err := milvusclient.New(ctx, &milvusclient.ClientConfig{
		Address: addr,
	})
	if err != nil {
		return nil, fmt.Errorf("milvus: connect to %s: %w", addr, err)
	}
	return &Client{
		cli:        cli,
		collection: collectionName,
		embedDim:   embedDim,
	}, nil
}

// Close releases the underlying gRPC connection.
func (c *Client) Close(ctx context.Context) error {
	return c.cli.Close(ctx)
}

// EnsureCollection creates the collection and vector index when they don't
// exist, then loads the collection into memory for search.
func (c *Client) EnsureCollection(ctx context.Context) error {
	has, err := c.cli.HasCollection(ctx, milvusclient.NewHasCollectionOption(c.collection))
	if err != nil {
		return fmt.Errorf("milvus: check collection: %w", err)
	}
	if !has {
		schema := entity.NewSchema().
			WithField(entity.NewField().WithName(fieldID).WithDataType(entity.FieldTypeVarChar).WithIsPrimaryKey(true).WithMaxLength(64)).
			WithField(entity.NewField().WithName(fieldPeerID).WithDataType(entity.FieldTypeVarChar).WithMaxLength(256)).
			WithField(entity.NewField().WithName(fieldContent).WithDataType(entity.FieldTypeVarChar).WithMaxLength(65535)).
			WithField(entity.NewField().WithName(fieldTags).WithDataType(entity.FieldTypeVarChar).WithMaxLength(1024)).
			WithField(entity.NewField().WithName(fieldCategory).WithDataType(entity.FieldTypeVarChar).WithMaxLength(256)).
			WithField(entity.NewField().WithName(fieldEmbedding).WithDataType(entity.FieldTypeFloatVector).WithDim(int64(c.embedDim)))

		if err := c.cli.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(c.collection, schema)); err != nil {
			return fmt.Errorf("milvus: create collection: %w", err)
		}

		// Create HNSW index on the embedding field.
		idx := index.NewHNSWIndex(entity.COSINE, 16, 256)
		task, err := c.cli.CreateIndex(ctx, milvusclient.NewCreateIndexOption(c.collection, fieldEmbedding, idx))
		if err != nil {
			return fmt.Errorf("milvus: create index: %w", err)
		}
		if err := task.Await(ctx); err != nil {
			return fmt.Errorf("milvus: await index: %w", err)
		}
	}

	// Only load when not already loaded.
	state, err := c.cli.GetLoadState(ctx, milvusclient.NewGetLoadStateOption(c.collection))
	if err != nil {
		return fmt.Errorf("milvus: get load state: %w", err)
	}
	if state.State != entity.LoadStateLoaded {
		loadTask, err := c.cli.LoadCollection(ctx, milvusclient.NewLoadCollectionOption(c.collection))
		if err != nil {
			return fmt.Errorf("milvus: load collection: %w", err)
		}
		if err := loadTask.Await(ctx); err != nil {
			return fmt.Errorf("milvus: await load: %w", err)
		}
	}
	return nil
}

// Upsert inserts or updates memory chunks in the collection.
// All slices must be the same length; embeddings must not be empty.
func (c *Client) Upsert(ctx context.Context, ids, documents []string, embeddings [][]float32, metadatas []map[string]any) error {
	if len(ids) == 0 {
		return nil
	}
	if len(embeddings) != len(ids) {
		return fmt.Errorf("milvus: upsert: embeddings count %d != ids count %d", len(embeddings), len(ids))
	}

	peerIDs := make([]string, len(ids))
	tags := make([]string, len(ids))
	categories := make([]string, len(ids))
	for i, m := range metadatas {
		if pid, ok := m["peer_id"].(string); ok {
			peerIDs[i] = pid
		}
		if t, ok := m["tags"].(string); ok {
			tags[i] = t
		}
		if cat, ok := m["category"].(string); ok {
			categories[i] = cat
		}
	}

	_, err := c.cli.Upsert(ctx, milvusclient.NewColumnBasedInsertOption(c.collection,
		column.NewColumnVarChar(fieldID, ids),
		column.NewColumnVarChar(fieldPeerID, peerIDs),
		column.NewColumnVarChar(fieldContent, documents),
		column.NewColumnVarChar(fieldTags, tags),
		column.NewColumnVarChar(fieldCategory, categories),
		column.NewColumnFloatVector(fieldEmbedding, c.embedDim, embeddings),
	))
	if err != nil {
		return fmt.Errorf("milvus: upsert: %w", err)
	}
	return nil
}

// Query performs a cosine-similarity search restricted to the given peer.
// It returns at most nResults results ordered by descending similarity.
func (c *Client) Query(ctx context.Context, queryEmbedding []float32, nResults int, peerID string) ([]QueryResult, error) {
	if len(queryEmbedding) == 0 {
		return nil, fmt.Errorf("milvus: query requires an embedding vector")
	}
	if nResults <= 0 {
		nResults = 10
	}

	filter := fmt.Sprintf(`%s == %q`, fieldPeerID, peerID)
	searchOpt := milvusclient.NewSearchOption(
		c.collection, nResults,
		[]entity.Vector{entity.FloatVector(queryEmbedding)},
	).
		WithFilter(filter).
		WithOutputFields(fieldID, fieldPeerID, fieldContent, fieldTags, fieldCategory).
		WithANNSField(fieldEmbedding)

	results, err := c.cli.Search(ctx, searchOpt)
	if err != nil {
		return nil, fmt.Errorf("milvus: search: %w", err)
	}
	if len(results) == 0 || results[0].ResultCount == 0 {
		return nil, nil
	}

	rs := results[0]
	out := make([]QueryResult, rs.ResultCount)
	idCol := rs.GetColumn(fieldID)
	contentCol := rs.GetColumn(fieldContent)
	peerCol := rs.GetColumn(fieldPeerID)

	for i := 0; i < rs.ResultCount; i++ {
		out[i].Metadata = make(map[string]any)

		if idCol != nil {
			if v, err := idCol.GetAsString(i); err == nil {
				out[i].ID = v
			}
		}
		if contentCol != nil {
			if v, err := contentCol.GetAsString(i); err == nil {
				out[i].Document = v
			}
		}
		if peerCol != nil {
			if v, err := peerCol.GetAsString(i); err == nil {
				out[i].Metadata["peer_id"] = v
			}
		}
		if i < len(rs.Scores) {
			out[i].Score = rs.Scores[i]
		}
	}
	return out, nil
}

// Delete removes chunks by their IDs from the collection.
func (c *Client) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := c.cli.Delete(ctx,
		milvusclient.NewDeleteOption(c.collection).WithStringIDs(fieldID, ids),
	)
	if err != nil {
		return fmt.Errorf("milvus: delete: %w", err)
	}
	return nil
}

// Count returns the approximate number of entities in the collection.
func (c *Client) Count(ctx context.Context) (int, error) {
	stats, err := c.cli.GetCollectionStats(ctx, milvusclient.NewGetCollectionStatsOption(c.collection))
	if err != nil {
		return 0, fmt.Errorf("milvus: count: %w", err)
	}
	if v, ok := stats["row_count"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("milvus: count: parse row_count: %w", err)
		}
		return n, nil
	}
	return 0, nil
}

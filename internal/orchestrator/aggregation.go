package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/redact"
	"github.com/ffimnsr/koios/internal/types"
)

// aggregate assembles the final reply from all run.Children and records provenance.
func (o *Orchestrator) aggregate(ctx context.Context, run *Run, req FanOutRequest) (string, error) {
	run.mu.Lock()
	children := append([]ChildResult(nil), run.Children...)
	run.mu.Unlock()
	return o.aggregateWith(ctx, run, req, children)
}

// aggregateWith aggregates a caller-supplied children slice and records provenance.
// It is the canonical implementation; aggregate() is a thin wrapper over it.
func (o *Orchestrator) aggregateWith(ctx context.Context, run *Run, req FanOutRequest, children []ChildResult) (string, error) {
	start := time.Now()

	inputLength := 0
	for _, c := range children {
		inputLength += len(c.FinalReply)
	}

	var result string
	var err error
	var reducerSessionKey string

	switch req.Aggregation {
	case AggregateCollect:
		result = aggregateCollect(children)
	case AggregateConcat:
		result = aggregateConcat(children)
	case AggregateReducer:
		reducerSessionKey = fmt.Sprintf("%s::orchestrator-reducer::%s", req.PeerID, run.ID)
		result, err = o.aggregateReducer(ctx, run, req, children)
	case AggregateVote:
		result, err = o.aggregateVote(ctx, run, req, children)
	default:
		result = aggregateCollect(children)
	}

	prov := &AggregationProvenance{
		Mode:              req.Aggregation,
		ReducerSessionKey: reducerSessionKey,
		InputLength:       inputLength,
		OutputLength:      len(result),
		DurationMs:        time.Since(start).Milliseconds(),
	}
	run.mu.Lock()
	run.Provenance = prov
	run.mu.Unlock()

	return result, err
}

func aggregateCollect(children []ChildResult) string {
	var parts []string
	for _, c := range children {
		if c.FinalReply != "" {
			parts = append(parts, fmt.Sprintf("[%s]: %s", c.Label, c.FinalReply))
		} else if c.Error != "" {
			parts = append(parts, fmt.Sprintf("[%s]: error: %s", c.Label, c.Error))
		}
	}
	return strings.Join(parts, "\n\n")
}

func aggregateConcat(children []ChildResult) string {
	var sb strings.Builder
	for i, c := range children {
		if i > 0 {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## ")
		sb.WriteString(c.Label)
		sb.WriteString("\n\n")
		if c.FinalReply != "" {
			sb.WriteString(c.FinalReply)
		} else if c.Error != "" {
			sb.WriteString("Error: ")
			sb.WriteString(c.Error)
		} else {
			sb.WriteString("(no output)")
		}
	}
	return sb.String()
}

// aggregateReducer runs a final LLM agent pass over all child results.
// The raw labeled child outputs are preserved in run.Children; the synthesized
// reply is returned as the aggregated result.
func (o *Orchestrator) aggregateReducer(ctx context.Context, run *Run, req FanOutRequest, children []ChildResult) (string, error) {
	if o.agentRT == nil {
		return aggregateConcat(children), nil
	}

	prompt := req.ReducerPrompt
	if prompt == "" {
		prompt = "Synthesize the following agent results into a single coherent reply. Preserve key findings from each contributor."
	}
	labeled := aggregateConcat(children)
	task := fmt.Sprintf("%s\n\nChild results:\n\n%s", prompt, labeled)

	sessionKey := fmt.Sprintf("%s::orchestrator-reducer::%s", req.PeerID, run.ID)
	result, err := o.agentRT.Run(ctx, agent.RunRequest{
		PeerID:     req.PeerID,
		Scope:      agent.ScopeIsolated,
		SessionKey: sessionKey,
		Messages: []types.Message{
			{Role: "user", Content: task},
		},
		Model: req.Model,
	})
	if err != nil {
		slog.Warn("orchestrator reducer failed, falling back to concat", "id", run.ID, "error", err)
		return aggregateConcat(children), fmt.Errorf("reducer: %w", err)
	}
	return redact.String(result.AssistantText), nil
}

// aggregateVote clusters child replies by cosine similarity and elects a
// winner. If no cluster achieves a strict majority, a single LLM tie-breaker
// call is made using the top-2 cluster summaries.
func (o *Orchestrator) aggregateVote(ctx context.Context, run *Run, req FanOutRequest, children []ChildResult) (string, error) {
	type entry struct {
		label string
		reply string
	}
	var entries []entry
	for _, c := range children {
		if c.FinalReply != "" {
			entries = append(entries, entry{c.Label, c.FinalReply})
		}
	}
	if len(entries) == 0 {
		return aggregateCollect(children), nil
	}

	threshold := req.VoteConfig.AgreementThreshold
	if threshold <= 0 {
		threshold = 0.5
	}

	vec := make([]map[string]float64, len(entries))
	for i, e := range entries {
		vec[i] = wordFreq(e.reply)
	}

	type cluster struct {
		indices  []int
		centroid map[string]float64
	}
	var clusters []cluster
	for i, v := range vec {
		joined := false
		for ci := range clusters {
			if cosineSim(v, clusters[ci].centroid) >= threshold {
				clusters[ci].indices = append(clusters[ci].indices, i)
				clusters[ci].centroid = centroidAdd(clusters[ci].centroid, v, len(clusters[ci].indices))
				joined = true
				break
			}
		}
		if !joined {
			clusterCentroid := make(map[string]float64, len(v))
			for k, val := range v {
				clusterCentroid[k] = val
			}
			clusters = append(clusters, cluster{indices: []int{i}, centroid: clusterCentroid})
		}
	}

	best := 0
	for ci, cl := range clusters {
		if len(cl.indices) > len(clusters[best].indices) {
			best = ci
		}
	}

	total := len(entries)
	winnerCluster := clusters[best]

	for i := 1; i < len(clusters); i++ {
		for j := i; j > 0 && len(clusters[j].indices) > len(clusters[j-1].indices); j-- {
			clusters[j], clusters[j-1] = clusters[j-1], clusters[j]
		}
	}

	var winner string
	var usedLLM bool

	if len(winnerCluster.indices)*2 > total {
		winner = entries[winnerCluster.indices[0]].reply
	} else if o.agentRT != nil && len(clusters) >= 2 {
		top1 := entries[clusters[0].indices[0]].reply
		top2 := entries[clusters[1].indices[0]].reply
		model := req.VoteConfig.TiebreakerModel
		if model == "" {
			model = req.Model
		}
		task := fmt.Sprintf(
			"Two groups of agents produced these responses. Decide which is more correct and return it verbatim (no extra text).\n\nGroup A:\n%s\n\nGroup B:\n%s",
			top1, top2,
		)
		sessionKey := fmt.Sprintf("%s::orchestrator-tiebreaker::%s", req.PeerID, run.ID)
		result, err := o.agentRT.Run(ctx, agent.RunRequest{
			PeerID:     req.PeerID,
			Scope:      agent.ScopeIsolated,
			SessionKey: sessionKey,
			Messages:   []types.Message{{Role: "user", Content: task}},
			Model:      model,
		})
		if err != nil {
			slog.Warn("orchestrator vote tiebreaker failed, using largest cluster", "id", run.ID, "error", err)
			winner = entries[clusters[0].indices[0]].reply
		} else {
			winner = redact.String(result.AssistantText)
			usedLLM = true
		}
	} else {
		winner = entries[winnerCluster.indices[0]].reply
	}

	_ = usedLLM

	if !req.VoteConfig.DisagreementReport || len(clusters) <= 1 {
		return winner, nil
	}

	var sb strings.Builder
	sb.WriteString(winner)
	sb.WriteString("\n\n--- Dissenting views ---\n")
	for ci, cl := range clusters {
		if ci == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("Minority cluster (%d/%d replies):\n", len(cl.indices), total))
		for _, idx := range cl.indices {
			sb.WriteString(fmt.Sprintf("  [%s]: %s\n", entries[idx].label, entries[idx].reply))
		}
	}
	return sb.String(), nil
}

// wordFreq returns a normalised term-frequency map for a text string.
func wordFreq(s string) map[string]float64 {
	freq := make(map[string]float64)
	fields := strings.Fields(strings.ToLower(s))
	for _, w := range fields {
		w = strings.Trim(w, ".,!?;:\"'()[]{}")
		if w != "" {
			freq[w]++
		}
	}
	if len(fields) > 0 {
		n := float64(len(fields))
		for k := range freq {
			freq[k] /= n
		}
	}
	return freq
}

// cosineSim returns the cosine similarity between two word-frequency maps.
func cosineSim(a, b map[string]float64) float64 {
	var dot, normA, normB float64
	for k, va := range a {
		normA += va * va
		if vb, ok := b[k]; ok {
			dot += va * vb
		}
	}
	for _, vb := range b {
		normB += vb * vb
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// centroidAdd returns the running average centroid after adding the new vector
// as the n-th member.
func centroidAdd(centroid, new map[string]float64, n int) map[string]float64 {
	result := make(map[string]float64)
	keys := make(map[string]struct{}, len(centroid)+len(new))
	for k := range centroid {
		keys[k] = struct{}{}
	}
	for k := range new {
		keys[k] = struct{}{}
	}
	for k := range keys {
		result[k] = (centroid[k]*float64(n-1) + new[k]) / float64(n)
	}
	return result
}

// validateOutputSchema parses reply as JSON and checks that all keys declared
// in the schema's "required" array are present in the top-level object.
// Returns a list of violation strings (empty means valid).
func validateOutputSchema(reply, schema string) []string {
	var schemaDoc struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(schema), &schemaDoc); err != nil || len(schemaDoc.Required) == 0 {
		var out any
		if err2 := json.Unmarshal([]byte(reply), &out); err2 != nil {
			return []string{"reply is not valid JSON: " + err2.Error()}
		}
		return nil
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(reply), &obj); err != nil {
		return []string{"reply is not valid JSON object: " + err.Error()}
	}

	var violations []string
	for _, key := range schemaDoc.Required {
		if _, ok := obj[key]; !ok {
			violations = append(violations, fmt.Sprintf("missing required key %q", key))
		}
	}
	return violations
}

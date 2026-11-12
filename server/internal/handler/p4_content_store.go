package handler

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/redis/go-redis/v9"
)

const p4ContentRetention = 10 * time.Minute
const p4ContentTimeout = 2 * time.Minute
const p4ContentKeyPrefix = "mul:" + runtimePendingRedisHashTag + ":p4_content:req:"
const p4ContentPendingPrefix = "mul:" + runtimePendingRedisHashTag + ":p4_content:pending:"

type P4ContentRequest struct {
	ID           string                        `json:"id"`
	WorkspaceID  string                        `json:"workspace_id"`
	IssueID      string                        `json:"issue_id"`
	ChangelistID string                        `json:"changelist_id"`
	RuntimeID    string                        `json:"runtime_id"`
	UserID       string                        `json:"user_id"`
	File         string                        `json:"file"`
	Status       string                        `json:"status"`
	Result       *protocol.P4ChangelistContent `json:"result"`
	Error        string                        `json:"error"`
	CreatedAt    time.Time                     `json:"created_at"`
	UpdatedAt    time.Time                     `json:"updated_at"`
}

// P4ContentStore follows the runtime pending-work transport. Results expire;
// source code is not kept in the product database. Redis shares the queue and
// results across API nodes; a single-node server uses the in-memory store.
type P4ContentStore struct {
	mu       sync.Mutex
	requests map[string]P4ContentRequest
	rdb      redis.UniversalClient
}

func NewP4ContentStore(rdb redis.UniversalClient) *P4ContentStore {
	return &P4ContentStore{requests: make(map[string]P4ContentRequest), rdb: rdb}
}

func (s *P4ContentStore) Create(ctx context.Context, req P4ContentRequest) (*P4ContentRequest, error) {
	req.ID, req.Status = randomID(), "pending"
	req.CreatedAt, req.UpdatedAt = time.Now(), time.Now()
	if s.rdb != nil {
		data, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		queue := p4ContentPendingPrefix + req.RuntimeID
		pipe := s.rdb.Pipeline()
		pipe.Set(ctx, p4ContentKeyPrefix+req.ID, data, p4ContentRetention)
		pipe.ZAdd(ctx, queue, redis.Z{Score: float64(req.CreatedAt.UnixNano()), Member: req.ID})
		pipe.Expire(ctx, queue, 2*p4ContentRetention)
		if _, err := pipe.Exec(ctx); err != nil {
			_ = s.rdb.Del(ctx, p4ContentKeyPrefix+req.ID).Err()
			_ = s.rdb.ZRem(ctx, queue, req.ID).Err()
			return nil, err
		}
	} else {
		s.mu.Lock()
		defer s.mu.Unlock()
		for id, old := range s.requests {
			if time.Since(old.CreatedAt) > p4ContentRetention {
				delete(s.requests, id)
			}
		}
		s.requests[req.ID] = req
	}
	return &req, nil
}

func (s *P4ContentStore) Get(ctx context.Context, id string) (*P4ContentRequest, error) {
	var req P4ContentRequest
	if s.rdb != nil {
		data, err := s.rdb.Get(ctx, p4ContentKeyPrefix+id).Bytes()
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, err
		}
	} else {
		s.mu.Lock()
		stored, exists := s.requests[id]
		s.mu.Unlock()
		if !exists || time.Since(stored.CreatedAt) > p4ContentRetention {
			return nil, nil
		}
		req = stored
	}
	if (req.Status == "pending" || req.Status == "running") && time.Since(req.CreatedAt) > p4ContentTimeout {
		req.Status, req.Error = "failed", "Perforce content request timed out; check the daemon connection and retry"
	}
	return &req, nil
}

func (s *P4ContentStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	if s.rdb != nil {
		count, err := s.rdb.ZCard(ctx, p4ContentPendingPrefix+runtimeID).Result()
		return count > 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, req := range s.requests {
		if req.RuntimeID == runtimeID && req.Status == "pending" && time.Since(req.CreatedAt) <= p4ContentTimeout {
			return true, nil
		}
	}
	return false, nil
}

func (s *P4ContentStore) PopPending(ctx context.Context, runtimeID string) (*P4ContentRequest, error) {
	if s.rdb == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		var selected *P4ContentRequest
		for _, req := range s.requests {
			if req.RuntimeID == runtimeID && req.Status == "pending" && time.Since(req.CreatedAt) <= p4ContentTimeout && (selected == nil || req.CreatedAt.Before(selected.CreatedAt)) {
				copy := req
				selected = &copy
			}
		}
		if selected != nil {
			selected.Status, selected.UpdatedAt = "running", time.Now()
			s.requests[selected.ID] = *selected
		}
		return selected, nil
	}
	queue := p4ContentPendingPrefix + runtimeID
	for attempt := 0; attempt < 5; attempt++ {
		ids, err := s.rdb.ZRange(ctx, queue, 0, 0).Result()
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		req, err := s.Get(ctx, ids[0])
		if err != nil {
			return nil, err
		}
		if req == nil || req.Status != "pending" {
			if err := s.rdb.ZRem(ctx, queue, ids[0]).Err(); err != nil {
				return nil, err
			}
			continue
		}
		req.Status, req.UpdatedAt = "running", time.Now()
		data, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		won, err := claimPendingScript.Run(ctx, s.rdb, []string{queue, p4ContentKeyPrefix + req.ID}, req.ID, data, int(p4ContentRetention.Seconds())).Int64()
		if err != nil {
			return nil, err
		}
		if won == 1 {
			return req, nil
		}
	}
	return nil, nil
}

func (s *P4ContentStore) Complete(ctx context.Context, id string, result *protocol.P4ChangelistContent, message string) error {
	req, err := s.Get(ctx, id)
	if err != nil || req == nil || req.Status != "running" {
		return err
	}
	req.Status, req.Result, req.Error, req.UpdatedAt = "completed", result, message, time.Now()
	if message != "" {
		req.Status = "failed"
	}
	if s.rdb != nil {
		data, err := json.Marshal(req)
		if err != nil {
			return err
		}
		return s.rdb.Set(ctx, p4ContentKeyPrefix+id, data, p4ContentRetention).Err()
	}
	s.mu.Lock()
	s.requests[id] = *req
	s.mu.Unlock()
	return nil
}

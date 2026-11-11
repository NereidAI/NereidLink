// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// chainstore.go provides rule-chain version persistence: per-device group
// resolution, graph loading, draft version creation and activation.
// Groups themselves are managed in groupstore.go.

package rule

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChainStore loads rule chain versions and manages draft/active state.
type ChainStore struct {
	pool *pgxpool.Pool
}

// NewChainStore constructs the rule chain store.
func NewChainStore(pool *pgxpool.Pool) *ChainStore {
	return &ChainStore{pool: pool}
}

// ErrNoActiveChain indicates the resolved group has no active version.
var ErrNoActiveChain = fmt.Errorf("no active rule chain")

// DefaultGroupID returns the default chain group of a tenant.
func (s *ChainStore) DefaultGroupID(ctx context.Context, tenantID string) (string, error) {
	var groupID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM rule_chain_group
		 WHERE tenant_id = $1 AND is_default = TRUE`,
		tenantID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoActiveChain
	}
	if err != nil {
		return "", fmt.Errorf("find default group: %w", err)
	}
	return groupID, nil
}

// ChainForDevice resolves the chain group applicable to a device with the
// priority: device binding, then profile binding, then the tenant default
// group. A device binding fully replaces the profile binding. It returns
// the resolved group, its active version and the compiled-input graph.
func (s *ChainStore) ChainForDevice(ctx context.Context, tenantID, deviceID string) (string, string, GraphDTO, error) {
	var groupID, chainID string
	err := s.pool.QueryRow(ctx,
		`WITH chosen AS (
		     SELECT COALESCE(dg.id, pg.id, def.id) AS group_id
		     FROM device d
		     LEFT JOIN device_profile p
		            ON p.id = d.profile_id AND p.tenant_id = d.tenant_id
		     LEFT JOIN rule_chain_group dg
		            ON dg.id = d.rule_chain_group_id AND dg.enabled
		     LEFT JOIN rule_chain_group pg
		            ON pg.id = p.rule_chain_group_id AND pg.enabled
		     LEFT JOIN rule_chain_group def
		            ON def.tenant_id = d.tenant_id AND def.is_default
		               AND def.enabled
		     WHERE d.id = $1 AND d.tenant_id = $2
		 )
		 SELECT c.group_id, rc.id
		 FROM chosen c
		 JOIN rule_chain rc
		   ON rc.chain_group_id = c.group_id AND rc.state = 'ACTIVE'`,
		deviceID, tenantID).Scan(&groupID, &chainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", GraphDTO{}, ErrNoActiveChain
	}
	if err != nil {
		return "", "", GraphDTO{}, fmt.Errorf("resolve device chain: %w", err)
	}

	dto, err := s.Graph(ctx, chainID)
	return groupID, chainID, dto, err
}

// ActiveChainForGroup returns the active version graph of one group.
func (s *ChainStore) ActiveChainForGroup(ctx context.Context, groupID string) (string, GraphDTO, error) {
	var chainID string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM rule_chain
		 WHERE chain_group_id = $1 AND state = 'ACTIVE'`,
		groupID).Scan(&chainID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", GraphDTO{}, ErrNoActiveChain
	}
	if err != nil {
		return "", GraphDTO{}, fmt.Errorf("find active chain: %w", err)
	}

	dto, err := s.Graph(ctx, chainID)
	return chainID, dto, err
}

// Graph loads the full graph of a chain version.
func (s *ChainStore) Graph(ctx context.Context, chainID string) (GraphDTO, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, type, name, config::text, position_x, position_y
		 FROM rule_node WHERE chain_id = $1`, chainID)
	if err != nil {
		return GraphDTO{}, fmt.Errorf("load nodes: %w", err)
	}

	// Arrays must serialize as [], never null: the editor calls .map on
	// both segments unconditionally, including a draft that has nodes but
	// no edges (compilation is deferred to activation).
	var dto GraphDTO
	dto.Nodes = []NodeDTO{}
	dto.Edges = []EdgeDTO{}
	for rows.Next() {
		var node NodeDTO
		var config string
		if err := rows.Scan(&node.ID, &node.Type, &node.Name, &config,
			&node.PositionX, &node.PositionY); err != nil {
			return GraphDTO{}, fmt.Errorf("scan node: %w", err)
		}
		node.Config = []byte(config)
		dto.Nodes = append(dto.Nodes, node)
	}
	if err := rows.Err(); err != nil {
		return GraphDTO{}, err
	}
	rows.Close()

	edgeRows, err := s.pool.Query(ctx,
		`SELECT from_node::text, to_node::text, relation
		 FROM rule_edge WHERE chain_id = $1`, chainID)
	if err != nil {
		return GraphDTO{}, fmt.Errorf("load edges: %w", err)
	}
	defer edgeRows.Close()

	for edgeRows.Next() {
		var edge EdgeDTO
		if err := edgeRows.Scan(&edge.From, &edge.To, &edge.Relation); err != nil {
			return GraphDTO{}, fmt.Errorf("scan edge: %w", err)
		}
		dto.Edges = append(dto.Edges, edge)
	}
	if err := edgeRows.Err(); err != nil {
		return GraphDTO{}, err
	}
	return dto, nil
}

// CreateDraft stores a new immutable draft version of a chain group.
// Every node receives a fresh generated identifier; identifiers in the
// incoming graph are used only as local edge references, allowing one
// saved version to be edited and copied without colliding with its
// source rows.
func (s *ChainStore) CreateDraft(ctx context.Context, tenantID, groupID string, dto GraphDTO) (string, GraphDTO, error) {
	if err := s.EnsureGroupOwned(ctx, tenantID, groupID); err != nil {
		return "", GraphDTO{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", GraphDTO{}, fmt.Errorf("begin draft: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var name string
	if err := tx.QueryRow(ctx,
		`SELECT name FROM rule_chain_group WHERE id = $1`,
		groupID).Scan(&name); err != nil {
		return "", GraphDTO{}, fmt.Errorf("load group name: %w", err)
	}

	var version int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1
		 FROM rule_chain WHERE chain_group_id = $1`,
		groupID).Scan(&version); err != nil {
		return "", GraphDTO{}, fmt.Errorf("next version: %w", err)
	}

	chainID := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_chain
		   (id, tenant_id, name, version, state, is_root, chain_group_id)
		 VALUES ($1, $2, $3, $4, 'DRAFT', FALSE, $5)`,
		chainID, tenantID, name, version, groupID); err != nil {
		return "", GraphDTO{}, fmt.Errorf("insert draft chain: %w", err)
	}

	nodeIDs := map[string]string{}
	for index := range dto.Nodes {
		node := &dto.Nodes[index]
		sourceID := node.ID
		resolved := uuid.NewString()
		node.ID = resolved
		nodeIDs[sourceID] = resolved
		config := node.Config
		if len(config) == 0 {
			config = []byte("{}")
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO rule_node (id, chain_id, type, name, config,
			                        position_x, position_y)
			 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`,
			node.ID, chainID, node.Type, node.Name, string(config),
			node.PositionX, node.PositionY); err != nil {
			return "", GraphDTO{}, fmt.Errorf("insert node: %w", err)
		}
	}

	for index := range dto.Edges {
		edge := &dto.Edges[index]
		from := resolveReference(edge.From, nodeIDs)
		to := resolveReference(edge.To, nodeIDs)
		if from == "" || to == "" {
			return "", GraphDTO{}, fmt.Errorf("edge references unknown node: %s -> %s", edge.From, edge.To)
		}
		edge.From = from
		edge.To = to
		if _, err := tx.Exec(ctx,
			`INSERT INTO rule_edge (id, chain_id, from_node, to_node, relation)
			 VALUES ($1, $2, $3, $4, $5)`,
			uuid.NewString(), chainID, from, to, edge.Relation); err != nil {
			return "", GraphDTO{}, fmt.Errorf("insert edge: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return "", GraphDTO{}, fmt.Errorf("commit draft: %w", err)
	}
	// Normalize for the response: a segment absent in the request (e.g. a
	// chain with nodes but no edges) must echo [] instead of null.
	if dto.Nodes == nil {
		dto.Nodes = []NodeDTO{}
	}
	if dto.Edges == nil {
		dto.Edges = []EdgeDTO{}
	}
	return chainID, dto, nil
}

// Activate archives the currently active version of the group that owns
// chainID and activates chainID. Messages already in flight continue on
// the old cached version while new messages pick up the new one after
// cache invalidation.
func (s *ChainStore) Activate(ctx context.Context, tenantID, chainID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin activation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var groupID string
	if err := tx.QueryRow(ctx,
		`SELECT chain_group_id FROM rule_chain
		 WHERE id = $1 AND tenant_id = $2`,
		chainID, tenantID).Scan(&groupID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrChainNotFound
		}
		return fmt.Errorf("find chain version: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE rule_chain SET state = 'ARCHIVED'
		 WHERE chain_group_id = $1 AND state = 'ACTIVE'`,
		groupID); err != nil {
		return fmt.Errorf("archive active chain: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE rule_chain SET state = 'ACTIVE' WHERE id = $1`, chainID); err != nil {
		return fmt.Errorf("activate chain: %w", err)
	}

	return tx.Commit(ctx)
}

// ErrChainNotFound indicates a chain version does not exist.
var ErrChainNotFound = errors.New("rule chain version not found")

// GroupOfChain returns the group owning one chain version.
func (s *ChainStore) GroupOfChain(ctx context.Context, tenantID, chainID string) (string, error) {
	var groupID string
	err := s.pool.QueryRow(ctx,
		`SELECT chain_group_id FROM rule_chain
		 WHERE id = $1 AND tenant_id = $2`,
		chainID, tenantID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrChainNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load chain group: %w", err)
	}
	return groupID, nil
}

// resolveReference maps a graph-document node reference to its stored ID.
func resolveReference(reference string, nodeIDs map[string]string) string {
	if id, ok := nodeIDs[reference]; ok {
		return id
	}
	return ""
}

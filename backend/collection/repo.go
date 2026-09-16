package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/aceaura/model-surge-relay/backend/upstreamclient"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Catalog 是 repo 需要的最小目录读取能力（由 upstreamclient 满足）。
type Catalog interface {
	Models(ctx context.Context) ([]upstreamclient.Listing, error)
}

type Repo struct {
	pool    *pgxpool.Pool
	cache   *cache.Cache
	catalog Catalog
}

func NewRepo(pool *pgxpool.Pool, c *cache.Cache, catalog Catalog) *Repo {
	return &Repo{pool: pool, cache: c, catalog: catalog}
}

func (r *Repo) Create(ctx context.Context, name, note string) (Collection, error) {
	if strings.TrimSpace(name) == "" {
		return Collection{}, apperr.Field(apperr.InvalidRequest, "name", "name is required")
	}
	var out Collection
	err := r.pool.QueryRow(ctx,
		`INSERT INTO collections (name, note) VALUES ($1, $2)
		 RETURNING name, note, created_at, updated_at`, name, note).
		Scan(&out.Name, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	if isUniqueViolation(err) {
		return Collection{}, apperr.New(apperr.Conflict, fmt.Sprintf("collection %q already exists", name))
	}
	if err != nil {
		return Collection{}, err
	}
	return out, nil
}

func (r *Repo) Get(ctx context.Context, name string) (Collection, error) {
	var out Collection
	err := r.pool.QueryRow(ctx,
		`SELECT name, note, created_at, updated_at FROM collections WHERE name = $1`, name).
		Scan(&out.Name, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Collection{}, apperr.New(apperr.NotFound, fmt.Sprintf("collection %q not found", name))
	}
	if err != nil {
		return Collection{}, err
	}
	return out, nil
}

func (r *Repo) List(ctx context.Context) ([]Collection, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT name, note, created_at, updated_at FROM collections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.Name, &c.Note, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repo) UpdateNote(ctx context.Context, name, note string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE collections SET note = $2, updated_at = now() WHERE name = $1`, name, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("collection %q not found", name))
	}
	r.invalidate(ctx, name)
	return nil
}

// Delete 级联删除该 Collection 下的全部 Group 与成员（由外键 ON DELETE CASCADE 保证）。
// 仍被 user model 引用时外键会拒绝，转为 conflict。
func (r *Repo) Delete(ctx context.Context, name string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM collections WHERE name = $1`, name)
	if isForeignKeyViolation(err) {
		return apperr.New(apperr.Conflict,
			fmt.Sprintf("collection %q is still referenced by user models", name))
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("collection %q not found", name))
	}
	r.invalidate(ctx, name)
	return nil
}

func (r *Repo) CreateGroup(ctx context.Context, g Group) (Group, error) {
	if strings.TrimSpace(g.Name) == "" {
		return Group{}, apperr.Field(apperr.InvalidRequest, "name", "group name is required")
	}
	if strings.TrimSpace(g.Type) == "" {
		return Group{}, apperr.Field(apperr.InvalidRequest, "type", "group type is required")
	}
	if _, err := r.Get(ctx, g.Collection); err != nil {
		return Group{}, err
	}
	config := g.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO groups (collection, name, type, position, config) VALUES ($1, $2, $3, $4, $5)`,
		g.Collection, g.Name, g.Type, g.Position, config)
	if isUniqueViolation(err) {
		return Group{}, apperr.New(apperr.Conflict,
			fmt.Sprintf("group %q already exists in collection %q", g.Name, g.Collection))
	}
	if err != nil {
		return Group{}, err
	}
	r.invalidate(ctx, g.Collection)
	g.Config = config
	if g.Members == nil {
		g.Members = []string{}
	}
	return g, nil
}

func (r *Repo) UpdateGroup(ctx context.Context, g Group) error {
	if strings.TrimSpace(g.Type) == "" {
		return apperr.Field(apperr.InvalidRequest, "type", "group type is required")
	}
	config := g.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE groups SET type = $3, position = $4, config = $5, updated_at = now()
		 WHERE collection = $1 AND name = $2`,
		g.Collection, g.Name, g.Type, g.Position, config)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound,
			fmt.Sprintf("group %q not found in collection %q", g.Name, g.Collection))
	}
	r.invalidate(ctx, g.Collection)
	return nil
}

func (r *Repo) DeleteGroup(ctx context.Context, collectionName, groupName string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM groups WHERE collection = $1 AND name = $2`, collectionName, groupName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound,
			fmt.Sprintf("group %q not found in collection %q", groupName, collectionName))
	}
	r.invalidate(ctx, collectionName)
	return nil
}

func (r *Repo) Groups(ctx context.Context, collectionName string) ([]Group, error) {
	if _, err := r.Get(ctx, collectionName); err != nil {
		return nil, err
	}
	return r.groups(ctx, collectionName)
}

func (r *Repo) groups(ctx context.Context, collectionName string) ([]Group, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT name, type, position, config FROM groups
		 WHERE collection = $1 ORDER BY position, name`, collectionName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []Group{}
	for rows.Next() {
		g := Group{Collection: collectionName, Members: []string{}}
		if err := rows.Scan(&g.Name, &g.Type, &g.Position, &g.Config); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	members, err := r.membersByGroup(ctx, collectionName)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if list, ok := members[groups[i].Name]; ok {
			groups[i].Members = list
		}
	}
	return groups, nil
}

func (r *Repo) membersByGroup(ctx context.Context, collectionName string) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT group_name, model_id FROM group_members
		 WHERE collection = $1 ORDER BY group_name, position, model_id`, collectionName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var group, modelID string
		if err := rows.Scan(&group, &modelID); err != nil {
			return nil, err
		}
		out[group] = append(out[group], modelID)
	}
	return out, rows.Err()
}

// ReplaceMembers 整体替换某组成员并保留给定顺序。
// 每个引用都先经 upstream 目录校验，未知引用整批拒绝。
func (r *Repo) ReplaceMembers(ctx context.Context, collectionName, groupName string, modelIDs []string) error {
	if err := r.validateReferences(ctx, modelIDs); err != nil {
		return err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM groups WHERE collection = $1 AND name = $2)`,
		collectionName, groupName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return apperr.New(apperr.NotFound,
			fmt.Sprintf("group %q not found in collection %q", groupName, collectionName))
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM group_members WHERE collection = $1 AND group_name = $2`,
		collectionName, groupName); err != nil {
		return err
	}
	for i, modelID := range modelIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO group_members (collection, group_name, model_id, position)
			 VALUES ($1, $2, $3, $4)`, collectionName, groupName, modelID, i); err != nil {
			if isUniqueViolation(err) {
				return apperr.Field(apperr.InvalidRequest, "members",
					fmt.Sprintf("duplicate member %q", modelID))
			}
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	r.invalidate(ctx, collectionName)
	return nil
}

// validateReferences 拒绝 upstream 目录中不存在的引用。
// 目录本身不可达时不放行——放行会让坏引用悄悄进库。
func (r *Repo) validateReferences(ctx context.Context, modelIDs []string) error {
	if len(modelIDs) == 0 {
		return nil
	}
	listings, err := r.catalog.Models(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(listings))
	for _, l := range listings {
		known[l.ID] = true
	}
	unknown := []string{}
	for _, id := range modelIDs {
		if !known[id] {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return apperr.Field(apperr.InvalidRequest, "members",
			fmt.Sprintf("unknown upstream model reference: %s", strings.Join(unknown, ", ")))
	}
	return nil
}

// Snapshot 产出调度用快照：成员引用连接 upstream 目录属性。
// 走 read-through 缓存；缓存不可用时直读 PG。
func (r *Repo) Snapshot(ctx context.Context, collectionName string) (Snapshot, error) {
	return cache.ReadThrough(ctx, r.cache, cache.CollectionKey(collectionName),
		func() (Snapshot, error) { return r.buildSnapshot(ctx, collectionName) })
}

func (r *Repo) buildSnapshot(ctx context.Context, collectionName string) (Snapshot, error) {
	if _, err := r.Get(ctx, collectionName); err != nil {
		return Snapshot{}, err
	}
	groups, err := r.groups(ctx, collectionName)
	if err != nil {
		return Snapshot{}, err
	}
	listings, err := r.catalog.Models(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	byID := make(map[string]upstreamclient.Listing, len(listings))
	for _, l := range listings {
		byID[l.ID] = l
	}

	snap := Snapshot{Name: collectionName, Groups: make([]GroupSnapshot, 0, len(groups))}
	for _, g := range groups {
		gs := GroupSnapshot{
			Name:     g.Name,
			Type:     g.Type,
			Position: g.Position,
			Config:   g.Config,
			Members:  make([]Member, 0, len(g.Members)),
		}
		for i, modelID := range g.Members {
			m := Member{ModelID: modelID, Position: i}
			if l, ok := byID[modelID]; ok {
				m.Account = l.Account
				m.ProviderID = l.ProviderID
				m.Protocol = l.Protocol
				m.NativeModel = l.NativeModel
				m.ContextWindow = l.ContextWindow
				m.Enabled = l.Enabled
				m.Known = true
			}
			gs.Members = append(gs.Members, m)
		}
		snap.Groups = append(snap.Groups, gs)
	}
	sort.SliceStable(snap.Groups, func(i, j int) bool {
		if snap.Groups[i].Position != snap.Groups[j].Position {
			return snap.Groups[i].Position < snap.Groups[j].Position
		}
		return snap.Groups[i].Name < snap.Groups[j].Name
	})
	return snap, nil
}

func (r *Repo) invalidate(ctx context.Context, collectionName string) {
	r.cache.Invalidate(ctx, cache.CollectionKey(collectionName))
}

func isUniqueViolation(err error) bool { return pgErrorCode(err) == "23505" }

func isForeignKeyViolation(err error) bool { return pgErrorCode(err) == "23503" }

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

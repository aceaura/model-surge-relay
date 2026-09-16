package usermodel

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool  *pgxpool.Pool
	cache *cache.Cache
}

func NewRepo(pool *pgxpool.Pool, c *cache.Cache) *Repo {
	return &Repo{pool: pool, cache: c}
}

func (r *Repo) Create(ctx context.Context, m UserModel) (UserModel, error) {
	if err := validate(m); err != nil {
		return UserModel{}, err
	}
	var out UserModel
	err := r.pool.QueryRow(ctx,
		`INSERT INTO user_models (name, collection, policy, client_key, protocol, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING name, collection, coalesce(policy, ''), client_key, protocol, enabled, created_at, updated_at`,
		m.Name, m.Collection, nullable(m.Policy), m.ClientKey, m.Protocol, m.Enabled).
		Scan(&out.Name, &out.Collection, &out.Policy, &out.ClientKey, &out.Protocol, &out.Enabled,
			&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return UserModel{}, mapWriteError(err, m)
	}
	return out, nil
}

// Update 整体替换可变字段。Collection 与 policy 的存在性由外键把关，
// 违约转成可读的 invalid_request。
func (r *Repo) Update(ctx context.Context, m UserModel) (UserModel, error) {
	if err := validate(m); err != nil {
		return UserModel{}, err
	}
	var out UserModel
	err := r.pool.QueryRow(ctx,
		`UPDATE user_models SET collection = $2, policy = $3, client_key = $4,
		   protocol = $5, enabled = $6, updated_at = now()
		 WHERE name = $1
		 RETURNING name, collection, coalesce(policy, ''), client_key, protocol, enabled, created_at, updated_at`,
		m.Name, m.Collection, nullable(m.Policy), m.ClientKey, m.Protocol, m.Enabled).
		Scan(&out.Name, &out.Collection, &out.Policy, &out.ClientKey, &out.Protocol, &out.Enabled,
			&out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserModel{}, apperr.New(apperr.NotFound, fmt.Sprintf("user model %q not found", m.Name))
	}
	if err != nil {
		return UserModel{}, mapWriteError(err, m)
	}
	r.cache.Invalidate(ctx, cache.UserModelKey(m.Name))
	return out, nil
}

// cached 是 user model 的缓存形态。UserModel.ClientKey 标了 json:"-"
// 以免流入响应与日志，但缓存副本必须带上它，否则命中缓存后密钥为空、
// 鉴权全数失败。对外形态与缓存形态由此分成两个类型，各自只做一件事。
type cached struct {
	Model     UserModel `json:"model"`
	ClientKey string    `json:"client_key"`
}

func (r *Repo) Get(ctx context.Context, name string) (UserModel, error) {
	entry, err := cache.ReadThrough(ctx, r.cache, cache.UserModelKey(name),
		func() (cached, error) {
			m, err := r.load(ctx, name)
			if err != nil {
				return cached{}, err
			}
			return cached{Model: m, ClientKey: m.ClientKey}, nil
		})
	if err != nil {
		return UserModel{}, err
	}
	m := entry.Model
	m.ClientKey = entry.ClientKey
	return m, nil
}

func (r *Repo) load(ctx context.Context, name string) (UserModel, error) {
	var out UserModel
	err := r.pool.QueryRow(ctx,
		`SELECT name, collection, coalesce(policy, ''), client_key, protocol, enabled, created_at, updated_at
		 FROM user_models WHERE name = $1`, name).
		Scan(&out.Name, &out.Collection, &out.Policy, &out.ClientKey, &out.Protocol, &out.Enabled,
			&out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserModel{}, apperr.New(apperr.NotFound, fmt.Sprintf("user model %q not found", name))
	}
	if err != nil {
		return UserModel{}, err
	}
	return out, nil
}

func (r *Repo) List(ctx context.Context) ([]UserModel, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT name, collection, coalesce(policy, ''), client_key, protocol, enabled, created_at, updated_at
		 FROM user_models ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserModel{}
	for rows.Next() {
		var m UserModel
		if err := rows.Scan(&m.Name, &m.Collection, &m.Policy, &m.ClientKey, &m.Protocol, &m.Enabled,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repo) Delete(ctx context.Context, name string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM user_models WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("user model %q not found", name))
	}
	r.cache.Invalidate(ctx, cache.UserModelKey(name))
	return nil
}

// Authenticate 是调度面的入口校验。返回的记录不再携带 ClientKey，
// 让密钥无法顺着调度链路继续往下流。
func (r *Repo) Authenticate(ctx context.Context, name, protocol, clientKey string) (UserModel, error) {
	m, err := r.Get(ctx, name)
	if err != nil {
		return UserModel{}, err
	}
	if !m.Enabled {
		return UserModel{}, apperr.New(apperr.Disabled, fmt.Sprintf("user model %q is disabled", name))
	}
	if subtle.ConstantTimeCompare([]byte(m.ClientKey), []byte(clientKey)) != 1 {
		return UserModel{}, apperr.New(apperr.Unauthorized, "client key mismatch")
	}
	// 未配置 protocol 表示不限入站协议。
	if m.Protocol != "" && protocol != "" && !strings.EqualFold(m.Protocol, protocol) {
		return UserModel{}, apperr.Field(apperr.InvalidRequest, "inbound_protocol",
			fmt.Sprintf("user model %q expects protocol %q, got %q", name, m.Protocol, protocol))
	}
	m.ClientKey = ""
	return m, nil
}

func validate(m UserModel) error {
	if strings.TrimSpace(m.Name) == "" {
		return apperr.Field(apperr.InvalidRequest, "name", "user model name is required")
	}
	if strings.TrimSpace(m.Collection) == "" {
		return apperr.Field(apperr.InvalidRequest, "collection", "collection is required")
	}
	if m.ClientKey == "" {
		return apperr.Field(apperr.InvalidRequest, "client_key", "client key is required")
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mapWriteError 把外键与唯一约束翻译成指向具体字段的校验错误。
// 约束名是判别依据：两个外键都可能违约，报错必须指对那一个。
func mapWriteError(err error, m UserModel) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505":
		return apperr.New(apperr.Conflict, fmt.Sprintf("user model %q already exists", m.Name))
	case "23503":
		if strings.Contains(pgErr.ConstraintName, "policy") {
			return apperr.Field(apperr.InvalidRequest, "policy",
				fmt.Sprintf("policy %q does not exist", m.Policy))
		}
		return apperr.Field(apperr.InvalidRequest, "collection",
			fmt.Sprintf("collection %q does not exist", m.Collection))
	default:
		return err
	}
}

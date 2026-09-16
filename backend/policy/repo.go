package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aceaura/model-surge-relay/backend/apperr"
	"github.com/aceaura/model-surge-relay/backend/cache"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Validator 是 repo 需要的最小编译校验能力（由 Engine 满足）。
type Validator interface {
	Validate(lang Language, source string) error
}

type Repo struct {
	pool      *pgxpool.Pool
	cache     *cache.Cache
	validator Validator
}

func NewRepo(pool *pgxpool.Pool, c *cache.Cache, v Validator) *Repo {
	return &Repo{pool: pool, cache: c, validator: v}
}

func (r *Repo) Create(ctx context.Context, p Policy) (Policy, error) {
	if strings.TrimSpace(p.Name) == "" {
		return Policy{}, apperr.Field(apperr.InvalidRequest, "name", "policy name is required")
	}
	lang := Language(strings.ToLower(strings.TrimSpace(string(p.Language))))
	if err := r.validator.Validate(lang, p.Source); err != nil {
		return Policy{}, err
	}
	var out Policy
	err := r.pool.QueryRow(ctx,
		`INSERT INTO policies (name, language, source, note)
		 VALUES ($1, $2, $3, $4)
		 RETURNING name, language, source, version, note, created_at, updated_at`,
		p.Name, string(lang), p.Source, p.Note).
		Scan(&out.Name, &out.Language, &out.Source, &out.Version, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	if isUniqueViolation(err) {
		return Policy{}, apperr.New(apperr.Conflict, fmt.Sprintf("policy %q already exists", p.Name))
	}
	if err != nil {
		return Policy{}, err
	}
	return out, nil
}

// Update 校验新源码后写入。版本只在源码变化时递增：编译缓存键含版本，
// 只改备注不该让全部副本重新编译。
func (r *Repo) Update(ctx context.Context, p Policy) (Policy, error) {
	lang := Language(strings.ToLower(strings.TrimSpace(string(p.Language))))
	if err := r.validator.Validate(lang, p.Source); err != nil {
		return Policy{}, err
	}
	var out Policy
	err := r.pool.QueryRow(ctx,
		`UPDATE policies SET
		   language = $2,
		   source   = $3,
		   note     = $4,
		   version  = version + CASE WHEN source <> $3 OR language <> $2 THEN 1 ELSE 0 END,
		   updated_at = now()
		 WHERE name = $1
		 RETURNING name, language, source, version, note, created_at, updated_at`,
		p.Name, string(lang), p.Source, p.Note).
		Scan(&out.Name, &out.Language, &out.Source, &out.Version, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, apperr.New(apperr.NotFound, fmt.Sprintf("policy %q not found", p.Name))
	}
	if err != nil {
		return Policy{}, err
	}
	r.cache.Invalidate(ctx, cache.PolicyKey(p.Name))
	return out, nil
}

func (r *Repo) Get(ctx context.Context, name string) (Policy, error) {
	return cache.ReadThrough(ctx, r.cache, cache.PolicyKey(name),
		func() (Policy, error) { return r.load(ctx, name) })
}

func (r *Repo) load(ctx context.Context, name string) (Policy, error) {
	var out Policy
	err := r.pool.QueryRow(ctx,
		`SELECT name, language, source, version, note, created_at, updated_at
		 FROM policies WHERE name = $1`, name).
		Scan(&out.Name, &out.Language, &out.Source, &out.Version, &out.Note, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Policy{}, apperr.New(apperr.NotFound, fmt.Sprintf("policy %q not found", name))
	}
	if err != nil {
		return Policy{}, err
	}
	return out, nil
}

func (r *Repo) List(ctx context.Context) ([]Policy, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT name, language, source, version, note, created_at, updated_at
		 FROM policies ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Policy{}
	for rows.Next() {
		var p Policy
		if err := rows.Scan(&p.Name, &p.Language, &p.Source, &p.Version, &p.Note, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete 先查引用者：外键报错只说"被引用"，列出名字才能让运维者知道去改哪个。
func (r *Repo) Delete(ctx context.Context, name string) error {
	refs, err := r.referencedBy(ctx, name)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return apperr.New(apperr.Conflict,
			fmt.Sprintf("policy %q is bound to user models: %s", name, strings.Join(refs, ", ")))
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM policies WHERE name = $1`, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperr.New(apperr.NotFound, fmt.Sprintf("policy %q not found", name))
	}
	r.cache.Invalidate(ctx, cache.PolicyKey(name))
	return nil
}

func (r *Repo) referencedBy(ctx context.Context, name string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT name FROM user_models WHERE policy = $1 ORDER BY name`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

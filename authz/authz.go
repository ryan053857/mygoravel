package authz

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

// AllTenants 是分配角色时表示“所有租户”的域，例如让运营人员在每个租户中都有只读支持权限。
// 它不包含平台本身（""）。
const AllTenants = "*"

var (
	// ErrNotMember 表示主体在本次请求的租户中没有任何角色。Resolve 返回它，请求得到 404，不暴露租户是否存在。
	ErrNotMember = errorx.Define(errorx.NotFound, "authz.not_member")
	// ErrRoleNotFound 表示角色不存在，或不能在该租户中查看。
	ErrRoleNotFound = errorx.Define(errorx.NotFound, "authz.role_not_found")
	// ErrRoleExists 表示同一租户中已有同名角色。
	ErrRoleExists = errorx.Define(errorx.Conflict, "authz.role_exists")
	// ErrInvalid 表示输入不合法，例如权限不在目录中、把租户自定义的角色分配到其他租户。
	ErrInvalid = errorx.Define(errorx.InvalidArgument, "authz.invalid")
)

// maxKey 是租户、主体 ID、角色名和权限名的最大长度，与 MySQL 迁移中 VARCHAR(191) 的列宽一致。
const maxKey = 191

// Permission 是权限目录中的一项。
type Permission struct {
	Name        string // 以冒号分隔的权限名，例如 articles:write
	Description string // 在角色编辑界面中显示的说明
}

// Catalog 是应用在代码中声明的全部权限。Store 只接受目录中的权限，以及能匹配目录中权限的通配符。
type Catalog []Permission

// Role 是一组权限。
type Role struct {
	ID          string
	Tenant      string // "" 为全局角色，可在任何租户和平台中分配；否则是该租户自定义的角色，只能在该租户中分配
	Name        string
	Description string
	Permissions []string // 支持通配符，见 identity.Subject.Can
}

// Member 是一个主体和它在某个域中的角色。
type Member struct {
	SubjectID string
	Roles     []Role
}

// Store 是基于 Bun 的角色与分配存储，查询兼容 PostgreSQL、MySQL 与 SQLite，
// Migrations 提供三种数据库的表结构。可以并发使用。
//
// 权限属于角色，角色在某个域中分配给主体。域是租户 ID、平台本身（""）或所有租户（AllTenants）。
// 主体在租户 t 中的权限，是它在 t 和 AllTenants 中全部角色的权限之和；在平台中的权限只来自 "" 中的角色。
type Store struct {
	db      bun.IDB
	catalog Catalog
	now     func() time.Time
}

type roleRow struct {
	bun.BaseModel `bun:"table:authz_roles,alias:r"`

	ID          uuid.UUID `bun:"id,pk,type:uuid"`
	Tenant      string    `bun:"tenant,notnull"`
	Name        string    `bun:"name,notnull"`
	Description string    `bun:"description,notnull"`
	CreatedAt   time.Time `bun:"created_at,notnull"`
	UpdatedAt   time.Time `bun:"updated_at,notnull"`
}

type permissionRow struct {
	bun.BaseModel `bun:"table:authz_role_permissions,alias:p"`

	RoleID     uuid.UUID `bun:"role_id,pk,type:uuid"`
	Permission string    `bun:"permission,pk"`
}

type assignmentRow struct {
	bun.BaseModel `bun:"table:authz_assignments,alias:a"`

	SubjectID string    `bun:"subject_id,pk"`
	Tenant    string    `bun:"tenant,pk"`
	RoleID    uuid.UUID `bun:"role_id,pk,type:uuid"`
	CreatedAt time.Time `bun:"created_at,notnull"`
}

// NewStore 创建存储。db 必须已执行 authz 迁移。catalog 是应用的权限目录，为空时 panic。
func NewStore(db bun.IDB, catalog Catalog) *Store {
	if db == nil {
		panic("authz: nil database")
	}
	if len(catalog) == 0 {
		panic("authz: empty permission catalog")
	}
	for _, p := range catalog {
		if err := checkName(p.Name, "permission"); err != nil || strings.Contains(p.Name, "*") {
			panic(errors.Newf("authz: invalid catalog permission %q", p.Name))
		}
	}
	return &Store{db: db, catalog: slices.Clone(catalog), now: time.Now}
}

// Catalog 返回权限目录，供角色编辑界面列出可选的权限。
func (s *Store) Catalog() Catalog { return slices.Clone(s.catalog) }

// Resolve 可以直接赋给 rest.Options.Resolve 和 web.Options.Resolve：按 identity.TenantFromContext 加载 sub
// 在本次请求的租户中的权限，写入 sub.Permissions 和 sub.Tenant。
// sub 在租户中没有任何角色时返回 ErrNotMember（404）；在平台中没有角色是正常的，权限为空。
func (s *Store) Resolve(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
	tenant := identity.TenantFromContext(ctx)
	perms, member, err := s.Permissions(ctx, sub.ID, tenant)
	if err != nil {
		return identity.Subject{}, err
	}
	if !member && tenant != "" {
		return identity.Subject{}, ErrNotMember.New()
	}
	sub.Tenant, sub.Permissions = tenant, perms
	return sub, nil
}

// Permissions 返回 subjectID 在 tenant 中的全部权限（去重、排序），以及它在其中是否有任何角色。
func (s *Store) Permissions(ctx context.Context, subjectID, tenant string) ([]string, bool, error) {
	var rows []struct {
		RoleID     uuid.UUID      `bun:"role_id"`
		Permission sql.NullString `bun:"permission"`
	}
	err := s.db.NewSelect().TableExpr("authz_assignments AS a").
		Join("LEFT JOIN authz_role_permissions AS p ON p.role_id = a.role_id").
		ColumnExpr("a.role_id, p.permission").
		Where("a.subject_id = ?", subjectID).Where("a.tenant IN (?)", bun.List(domains(tenant))).
		Scan(ctx, &rows)
	if err != nil {
		return nil, false, errors.Wrap(err, "authz: permissions")
	}
	var perms []string
	for _, r := range rows {
		if r.Permission.Valid {
			perms = append(perms, r.Permission.String)
		}
	}
	slices.Sort(perms)
	return slices.Compact(perms), len(rows) > 0, nil
}

// domains 返回租户的权限来自哪些域。
func domains(tenant string) []string {
	if tenant == "" {
		return []string{""}
	}
	return []string{tenant, AllTenants}
}

// CreateRole 创建角色，返回带 ID 的角色。权限必须在目录中，同一租户中的角色名不能重复。
func (s *Store) CreateRole(ctx context.Context, r Role) (Role, error) {
	if err := s.checkRole(r); err != nil {
		return Role{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Role{}, errors.Wrap(err, "authz: new id")
	}
	now := s.now().UTC()
	row := &roleRow{ID: id, Tenant: r.Tenant, Name: r.Name, Description: r.Description, CreatedAt: now, UpdatedAt: now}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := nameTaken(ctx, tx, r.Tenant, r.Name, uuid.Nil); err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
			return errors.Wrap(err, "authz: insert role")
		}
		return setPermissions(ctx, tx, id, r.Permissions)
	})
	if err != nil {
		return Role{}, err
	}
	r.ID, r.Permissions = id.String(), normalize(r.Permissions)
	return r, nil
}

// UpdateRole 修改角色的名称、说明和权限；r.ID 指定角色，角色所属的租户不能修改。
// 修改立即影响所有拥有这个角色的主体。
func (s *Store) UpdateRole(ctx context.Context, r Role) (Role, error) {
	id, err := uuid.Parse(r.ID)
	if err != nil {
		return Role{}, ErrRoleNotFound.New()
	}
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var row roleRow
		if err := tx.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx); errors.Is(err, sql.ErrNoRows) {
			return ErrRoleNotFound.New()
		} else if err != nil {
			return errors.Wrap(err, "authz: load role")
		}
		r.Tenant = row.Tenant
		if err := s.checkRole(r); err != nil {
			return err
		}
		if err := nameTaken(ctx, tx, r.Tenant, r.Name, id); err != nil {
			return err
		}
		_, err := tx.NewUpdate().Model((*roleRow)(nil)).
			Set("name = ?", r.Name).Set("description = ?", r.Description).Set("updated_at = ?", s.now().UTC()).
			Where("id = ?", id).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "authz: update role")
		}
		if _, err := tx.NewDelete().Model((*permissionRow)(nil)).Where("role_id = ?", id).Exec(ctx); err != nil {
			return errors.Wrap(err, "authz: clear permissions")
		}
		return setPermissions(ctx, tx, id, r.Permissions)
	})
	if err != nil {
		return Role{}, err
	}
	r.Permissions = normalize(r.Permissions)
	return r, nil
}

// DeleteRole 删除角色及其全部分配。角色不存在时返回 ErrRoleNotFound。
func (s *Store) DeleteRole(ctx context.Context, id string) error {
	rid, err := uuid.Parse(id)
	if err != nil {
		return ErrRoleNotFound.New()
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewDelete().Model((*roleRow)(nil)).Where("id = ?", rid).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "authz: delete role")
		}
		if n, err := res.RowsAffected(); err != nil {
			return errors.Wrap(err, "authz: delete role")
		} else if n == 0 {
			return ErrRoleNotFound.New()
		}
		for _, model := range []any{(*permissionRow)(nil), (*assignmentRow)(nil)} {
			if _, err := tx.NewDelete().Model(model).Where("role_id = ?", rid).Exec(ctx); err != nil {
				return errors.Wrap(err, "authz: delete role")
			}
		}
		return nil
	})
}

// Role 返回角色。tenant 不为空时，只返回全局角色或该租户的角色，其他租户的角色视为不存在，
// 供租户管理员的界面使用；tenant 为空时不限制。
func (s *Store) Role(ctx context.Context, id, tenant string) (Role, error) {
	rid, err := uuid.Parse(id)
	if err != nil {
		return Role{}, ErrRoleNotFound.New()
	}
	q := s.db.NewSelect().Model((*roleRow)(nil)).Where("id = ?", rid)
	if tenant != "" {
		q = q.Where("tenant IN (?)", bun.List([]string{"", tenant}))
	}
	roles, err := s.loadRoles(ctx, q)
	if err != nil {
		return Role{}, err
	}
	if len(roles) == 0 {
		return Role{}, ErrRoleNotFound.New()
	}
	return roles[0], nil
}

// Roles 返回 tenant 中可以分配的角色：全局角色，以及 tenant 自定义的角色（tenant 为空时只有全局角色），
// 全局角色在前，各自按名称排序。
func (s *Store) Roles(ctx context.Context, tenant string) ([]Role, error) {
	q := s.db.NewSelect().Model((*roleRow)(nil)).Where("tenant IN (?)", bun.List([]string{"", tenant}))
	return s.loadRoles(ctx, q)
}

// loadRoles 执行选择 authz_roles 的查询 q，并加载每个角色的权限。
func (s *Store) loadRoles(ctx context.Context, q *bun.SelectQuery) ([]Role, error) {
	var rows []roleRow
	if err := q.OrderExpr("tenant, name").Scan(ctx, &rows); err != nil {
		return nil, errors.Wrap(err, "authz: load roles")
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	var perms []permissionRow
	err := s.db.NewSelect().Model(&perms).Where("role_id IN (?)", bun.List(ids)).OrderExpr("permission").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "authz: load permissions")
	}
	byRole := map[uuid.UUID][]string{}
	for _, p := range perms {
		byRole[p.RoleID] = append(byRole[p.RoleID], p.Permission)
	}
	roles := make([]Role, len(rows))
	for i, r := range rows {
		roles[i] = Role{ID: r.ID.String(), Tenant: r.Tenant, Name: r.Name, Description: r.Description, Permissions: byRole[r.ID]}
	}
	return roles, nil
}

// Assign 在 tenant 中把角色分配给 subjectID；已经分配时什么也不做。
// tenant 为 "" 表示平台，AllTenants 表示所有租户。租户自定义的角色只能在该租户中分配，
// AllTenants 中只能分配全局角色，否则返回 ErrInvalid；角色不存在时返回 ErrRoleNotFound。
func (s *Store) Assign(ctx context.Context, subjectID, tenant, roleID string) error {
	if err := checkName(subjectID, "subject ID"); err != nil {
		return err
	}
	if len(tenant) > maxKey {
		return ErrInvalid.New(errorx.Public("field", "tenant"))
	}
	rid, err := uuid.Parse(roleID)
	if err != nil {
		return ErrRoleNotFound.New()
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var role roleRow
		if err := tx.NewSelect().Model(&role).Column("tenant").Where("id = ?", rid).Scan(ctx); errors.Is(err, sql.ErrNoRows) {
			return ErrRoleNotFound.New()
		} else if err != nil {
			return errors.Wrap(err, "authz: load role")
		}
		if role.Tenant != "" && role.Tenant != tenant {
			return ErrInvalid.New(errorx.Public("field", "tenant"))
		}
		exists, err := tx.NewSelect().Model((*assignmentRow)(nil)).
			Where("subject_id = ?", subjectID).Where("tenant = ?", tenant).Where("role_id = ?", rid).Exists(ctx)
		if err != nil {
			return errors.Wrap(err, "authz: assign")
		}
		if exists {
			return nil
		}
		row := &assignmentRow{SubjectID: subjectID, Tenant: tenant, RoleID: rid, CreatedAt: s.now().UTC()}
		_, err = tx.NewInsert().Model(row).Exec(ctx)
		return errors.Wrap(err, "authz: assign")
	})
}

// Unassign 撤销 subjectID 在 tenant 中的角色；没有分配时什么也不做。
func (s *Store) Unassign(ctx context.Context, subjectID, tenant, roleID string) error {
	rid, err := uuid.Parse(roleID)
	if err != nil {
		return nil
	}
	_, err = s.db.NewDelete().Model((*assignmentRow)(nil)).
		Where("subject_id = ?", subjectID).Where("tenant = ?", tenant).Where("role_id = ?", rid).Exec(ctx)
	return errors.Wrap(err, "authz: unassign")
}

// RemoveMember 撤销 subjectID 在 tenant 中的全部角色，返回撤销的个数，例如把成员移出租户。
func (s *Store) RemoveMember(ctx context.Context, subjectID, tenant string) (int, error) {
	res, err := s.db.NewDelete().Model((*assignmentRow)(nil)).
		Where("subject_id = ?", subjectID).Where("tenant = ?", tenant).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "authz: remove member")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "authz: remove member")
}

// RolesOf 返回 subjectID 在 tenant 这个域中直接分配的角色，不包括 AllTenants 中的角色。
func (s *Store) RolesOf(ctx context.Context, subjectID, tenant string) ([]Role, error) {
	members, err := s.members(ctx, tenant, subjectID)
	if err != nil || len(members) == 0 {
		return nil, err
	}
	return members[0].Roles, nil
}

// Members 返回在 tenant 这个域中有角色的全部主体及其角色，按主体 ID 排序，供成员管理界面使用。
func (s *Store) Members(ctx context.Context, tenant string) ([]Member, error) {
	return s.members(ctx, tenant, "")
}

// members 返回 tenant 中的成员；subjectID 不为空时只返回这个主体。
func (s *Store) members(ctx context.Context, tenant, subjectID string) ([]Member, error) {
	q := s.db.NewSelect().Model((*assignmentRow)(nil)).Column("subject_id", "role_id").Where("tenant = ?", tenant)
	if subjectID != "" {
		q = q.Where("subject_id = ?", subjectID)
	}
	var rows []assignmentRow
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, errors.Wrap(err, "authz: members")
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.RoleID
	}
	roles, err := s.loadRoles(ctx, s.db.NewSelect().Model((*roleRow)(nil)).Where("id IN (?)", bun.List(ids)))
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Role, len(roles))
	for _, r := range roles {
		byID[r.ID] = r
	}
	bySubject := map[string][]Role{}
	for _, r := range rows {
		bySubject[r.SubjectID] = append(bySubject[r.SubjectID], byID[r.RoleID.String()])
	}
	members := make([]Member, 0, len(bySubject))
	for id, roles := range bySubject {
		slices.SortFunc(roles, func(a, b Role) int { return cmp.Or(cmp.Compare(a.Tenant, b.Tenant), cmp.Compare(a.Name, b.Name)) })
		members = append(members, Member{SubjectID: id, Roles: roles})
	}
	slices.SortFunc(members, func(a, b Member) int { return cmp.Compare(a.SubjectID, b.SubjectID) })
	return members, nil
}

// Tenants 返回 subjectID 有角色的全部租户，按 ID 排序，不包括平台和 AllTenants，供租户切换界面使用。
func (s *Store) Tenants(ctx context.Context, subjectID string) ([]string, error) {
	var tenants []string
	err := s.db.NewSelect().Model((*assignmentRow)(nil)).Distinct().Column("tenant").
		Where("subject_id = ?", subjectID).Where("tenant NOT IN (?)", bun.List([]string{"", AllTenants})).
		OrderExpr("tenant").Scan(ctx, &tenants)
	return tenants, errors.Wrap(err, "authz: tenants")
}

// checkRole 检查角色的租户、名称和权限。
func (s *Store) checkRole(r Role) error {
	if r.Tenant == AllTenants || len(r.Tenant) > maxKey {
		return ErrInvalid.New(errorx.Public("field", "tenant"))
	}
	if err := checkName(r.Name, "name"); err != nil {
		return err
	}
	for _, p := range r.Permissions {
		if !s.known(p) {
			return ErrInvalid.New(errorx.Public("field", "permissions"), errorx.Public("permission", p))
		}
	}
	return nil
}

// known 报告 perm 是目录中的权限，或是能匹配目录中权限的通配符。
func (s *Store) known(perm string) bool {
	if perm == "*" {
		return true
	}
	if checkName(perm, "permission") != nil {
		return false
	}
	for _, p := range s.catalog {
		if identity.Grants([]string{perm}, p.Name) {
			return true
		}
	}
	return false
}

// checkName 检查名称不为空、不超过 maxKey 个字节，也不包含空白。
func checkName(name, field string) error {
	if name == "" || len(name) > maxKey || strings.ContainsFunc(name, unicode.IsSpace) {
		return ErrInvalid.New(errorx.Public("field", field))
	}
	return nil
}

// nameTaken 在同一租户中已有名为 name 的其他角色时返回 ErrRoleExists。
func nameTaken(ctx context.Context, tx bun.Tx, tenant, name string, except uuid.UUID) error {
	exists, err := tx.NewSelect().Model((*roleRow)(nil)).
		Where("tenant = ?", tenant).Where("name = ?", name).Where("id <> ?", except).Exists(ctx)
	if err != nil {
		return errors.Wrap(err, "authz: check role name")
	}
	if exists {
		return ErrRoleExists.New()
	}
	return nil
}

func setPermissions(ctx context.Context, tx bun.Tx, id uuid.UUID, perms []string) error {
	perms = normalize(perms)
	if len(perms) == 0 {
		return nil
	}
	rows := make([]permissionRow, len(perms))
	for i, p := range perms {
		rows[i] = permissionRow{RoleID: id, Permission: p}
	}
	_, err := tx.NewInsert().Model(&rows).Exec(ctx)
	return errors.Wrap(err, "authz: set permissions")
}

// normalize 返回去重并排序的权限。
func normalize(perms []string) []string {
	if len(perms) == 0 {
		return nil
	}
	out := slices.Clone(perms)
	slices.Sort(out)
	return slices.Compact(out)
}

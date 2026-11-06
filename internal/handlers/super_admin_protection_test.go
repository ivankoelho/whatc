package handlers_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// DEF-R4-001: an organization admin deleted the platform super admin
// (DELETE /api/users/{id} -> 200). The authority of the TARGET decides, not the
// organization the target happens to belong to, so every way of changing a
// super admin must be refused to anyone who is not a super admin (or the
// super admin themselves).

// superAdminFixture: an organization admin (org A) with the permissions the
// system "admin" role carries for users and members, a plain user in org A, and
// two kinds of super admin: one whose home is org A and one who is a guest of
// org A (home org B).
type superAdminFixture struct {
	app        *handlers.App
	orgA, orgB *models.Organization
	adminA     *models.User // org admin of A, not a super admin
	adminB     *models.User // org admin of B, not a member of A
	plainA     *models.User
	superHome  *models.User // super admin, home org A
	superGuest *models.User // super admin, home org B, guest member of A
	roleA      *models.CustomRole
}

func newSuperAdminFixture(t *testing.T) *superAdminFixture {
	t.Helper()
	app := newTestApp(t)
	f := &superAdminFixture{app: app}
	f.orgA = testutil.CreateTestOrganization(t, app.DB)
	f.orgB = testutil.CreateTestOrganization(t, app.DB)

	keys := []string{"users:read", "users:write", "users:delete", "organizations:assign"}
	f.roleA = testutil.CreateTestRoleWithKeys(t, app.DB, f.orgA.ID, "org-admin", keys)
	roleB := testutil.CreateTestRoleWithKeys(t, app.DB, f.orgB.ID, "org-admin", keys)

	f.adminA = testutil.CreateTestUser(t, app.DB, f.orgA.ID, testutil.WithRoleID(&f.roleA.ID))
	f.adminB = testutil.CreateTestUser(t, app.DB, f.orgB.ID, testutil.WithRoleID(&roleB.ID))
	f.plainA = testutil.CreateTestUser(t, app.DB, f.orgA.ID, testutil.WithRoleID(&f.roleA.ID))
	f.superHome = testutil.CreateTestUser(t, app.DB, f.orgA.ID, testutil.WithSuperAdmin())
	f.superGuest = testutil.CreateTestUser(t, app.DB, f.orgB.ID, testutil.WithSuperAdmin())
	require.NoError(t, app.DB.Create(&models.UserOrganization{
		BaseModel: models.BaseModel{ID: uuid.New()}, UserID: f.superGuest.ID,
		OrganizationID: f.orgA.ID, RoleID: &f.roleA.ID,
	}).Error)
	return f
}

func (f *superAdminFixture) userExists(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	var n int64
	require.NoError(t, f.app.DB.Model(&models.User{}).Where("id = ?", id).Count(&n).Error)
	return n == 1
}

func (f *superAdminFixture) memberships(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.app.DB.Model(&models.UserOrganization{}).Where("user_id = ?", id).Count(&n).Error)
	return n
}

// assertUntouched checks the account and its links are exactly as created.
func (f *superAdminFixture) assertUntouched(t *testing.T, u *models.User, wantLinks int64) {
	t.Helper()
	var got models.User
	require.NoError(t, f.app.DB.First(&got, "id = ?", u.ID).Error)
	assert.True(t, got.IsActive, "account must stay active")
	assert.True(t, got.IsSuperAdmin, "super admin flag must stay")
	assert.Equal(t, u.FullName, got.FullName)
	assert.Equal(t, u.PasswordHash, got.PasswordHash)
	assert.Equal(t, wantLinks, f.memberships(t, u.ID), "organization links must stay")
}

// assertNoAudit: a blocked attempt must not leave an audit trail of a change.
func (f *superAdminFixture) assertNoAudit(t *testing.T, target uuid.UUID) {
	t.Helper()
	time.Sleep(200 * time.Millisecond) // audit is written asynchronously
	var n int64
	require.NoError(t, f.app.DB.Model(&models.AuditLog{}).
		Where("resource_id = ?", target).Count(&n).Error)
	assert.Zero(t, n, "a refused request must not be recorded as a change")
}

func (f *superAdminFixture) del(t *testing.T, org *models.Organization, actor, target uuid.UUID) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(req, org.ID, actor)
	testutil.SetPathParam(req, "id", target.String())
	require.NoError(t, f.app.DeleteUser(req))
	return testutil.GetResponseStatusCode(req)
}

func (f *superAdminFixture) update(t *testing.T, org *models.Organization, actor, target uuid.UUID, body map[string]any) int {
	t.Helper()
	req := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(req, org.ID, actor)
	testutil.SetPathParam(req, "id", target.String())
	require.NoError(t, f.app.UpdateUser(req))
	return testutil.GetResponseStatusCode(req)
}

func TestSuperAdminProtection_OrgAdminCannotDeleteSuperAdmin(t *testing.T) {
	f := newSuperAdminFixture(t)

	// Home org A: this is the exact scenario that returned 200 in R4.
	assert.Equal(t, fasthttp.StatusForbidden, f.del(t, f.orgA, f.adminA.ID, f.superHome.ID))
	require.True(t, f.userExists(t, f.superHome.ID), "account must not be deleted")
	f.assertUntouched(t, f.superHome, 1)
	f.assertNoAudit(t, f.superHome.ID)

	// Guest of org A (home B): removing the membership is also refused.
	assert.Equal(t, fasthttp.StatusForbidden, f.del(t, f.orgA, f.adminA.ID, f.superGuest.ID))
	f.assertUntouched(t, f.superGuest, 2)
	f.assertNoAudit(t, f.superGuest.ID)
}

func TestSuperAdminProtection_OrgAdminCannotUpdateSuperAdmin(t *testing.T) {
	f := newSuperAdminFixture(t)

	for name, body := range map[string]map[string]any{
		"deactivate": {"is_active": false},
		"role":       {"role_id": f.roleA.ID.String()},
		"name":       {"full_name": "Pwned"},
		"seller":     {"xprocess_seller_code": "X1"},
		"credential": {"password": "taken-over-123"},
		"email":      {"email": "taken@example.com"},
	} {
		t.Run(name+"/home", func(t *testing.T) {
			assert.Equal(t, fasthttp.StatusForbidden, f.update(t, f.orgA, f.adminA.ID, f.superHome.ID, body))
		})
		t.Run(name+"/guest", func(t *testing.T) {
			assert.Equal(t, fasthttp.StatusForbidden, f.update(t, f.orgA, f.adminA.ID, f.superGuest.ID, body))
		})
	}
	f.assertUntouched(t, f.superHome, 1)
	f.assertUntouched(t, f.superGuest, 2)
	f.assertNoAudit(t, f.superHome.ID)
	f.assertNoAudit(t, f.superGuest.ID)
}

func TestSuperAdminProtection_MemberEndpointsRefuseSuperAdmin(t *testing.T) {
	f := newSuperAdminFixture(t)

	remove := testutil.NewJSONRequest(t, nil)
	testutil.SetAuthContext(remove, f.orgA.ID, f.adminA.ID)
	testutil.SetPathParam(remove, "member_id", f.superGuest.ID.String())
	require.NoError(t, f.app.RemoveOrganizationMember(remove))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(remove))

	role := testutil.NewJSONRequest(t, map[string]any{"role_id": f.roleA.ID.String()})
	testutil.SetAuthContext(role, f.orgA.ID, f.adminA.ID)
	testutil.SetPathParam(role, "member_id", f.superGuest.ID.String())
	require.NoError(t, f.app.UpdateOrganizationMemberRole(role))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(role))

	// Adding (or reviving) a super admin's membership rewrites its role.
	add := testutil.NewJSONRequest(t, map[string]any{"user_id": f.superHome.ID.String(), "role_id": f.roleA.ID.String()})
	testutil.SetAuthContext(add, f.orgA.ID, f.adminA.ID)
	require.NoError(t, f.app.AddOrganizationMember(add))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(add))

	f.assertUntouched(t, f.superGuest, 2)
	f.assertUntouched(t, f.superHome, 1)
	f.assertNoAudit(t, f.superGuest.ID)
}

// A deleted super admin's email must not be a way back in: CreateUser restores
// a soft-deleted account with a new password and no super admin flag.
func TestSuperAdminProtection_CreateUserCannotReviveSuperAdmin(t *testing.T) {
	f := newSuperAdminFixture(t)
	require.NoError(t, f.app.DB.Delete(&models.User{}, "id = ?", f.superHome.ID).Error)

	req := testutil.NewJSONRequest(t, map[string]any{
		"email": f.superHome.Email, "password": "taken-over-123",
		"full_name": "Pwned", "role_id": f.roleA.ID.String(),
	})
	testutil.SetAuthContext(req, f.orgA.ID, f.adminA.ID)
	require.NoError(t, f.app.CreateUser(req))
	assert.Equal(t, fasthttp.StatusForbidden, testutil.GetResponseStatusCode(req))

	var got models.User
	require.NoError(t, f.app.DB.Unscoped().First(&got, "id = ?", f.superHome.ID).Error)
	assert.True(t, got.DeletedAt.Valid, "account must stay deleted")
	assert.Equal(t, f.superHome.PasswordHash, got.PasswordHash)
	assert.True(t, got.IsSuperAdmin)
}

// Matrix rows that must keep working or stay blocked for other reasons.
func TestSuperAdminProtection_Matrix(t *testing.T) {
	t.Run("org admin A -> admin of org B is not reachable", func(t *testing.T) {
		f := newSuperAdminFixture(t)
		assert.Equal(t, fasthttp.StatusNotFound, f.del(t, f.orgA, f.adminA.ID, f.adminB.ID))
		assert.Equal(t, fasthttp.StatusNotFound, f.update(t, f.orgA, f.adminA.ID, f.adminB.ID, map[string]any{"is_active": false}))
		require.True(t, f.userExists(t, f.adminB.ID))
	})

	t.Run("org admin A -> plain user of A still works", func(t *testing.T) {
		f := newSuperAdminFixture(t)
		assert.Equal(t, fasthttp.StatusOK, f.update(t, f.orgA, f.adminA.ID, f.plainA.ID, map[string]any{"full_name": "Renamed"}))
		assert.Equal(t, fasthttp.StatusOK, f.del(t, f.orgA, f.adminA.ID, f.plainA.ID))
		assert.False(t, f.userExists(t, f.plainA.ID))
	})

	t.Run("plain user -> super admin is refused", func(t *testing.T) {
		f := newSuperAdminFixture(t)
		noPerm := testutil.CreateTestUser(t, f.app.DB, f.orgA.ID)
		assert.Equal(t, fasthttp.StatusForbidden, f.del(t, f.orgA, noPerm.ID, f.superHome.ID))
		assert.Equal(t, fasthttp.StatusForbidden, f.update(t, f.orgA, noPerm.ID, f.superHome.ID, map[string]any{"full_name": "x"}))
		f.assertUntouched(t, f.superHome, 1)
	})

	t.Run("super admin -> plain user is allowed", func(t *testing.T) {
		f := newSuperAdminFixture(t)
		assert.Equal(t, fasthttp.StatusOK, f.update(t, f.orgA, f.superHome.ID, f.plainA.ID, map[string]any{"full_name": "By super"}))
		assert.Equal(t, fasthttp.StatusOK, f.del(t, f.orgA, f.superHome.ID, f.plainA.ID))
	})

	// Platform policy (unchanged by this fix): a super admin may act on another
	// super admin. Pinned here so a later policy change is a conscious one.
	t.Run("super admin -> other super admin follows platform policy (allowed)", func(t *testing.T) {
		f := newSuperAdminFixture(t)
		assert.Equal(t, fasthttp.StatusOK, f.update(t, f.orgA, f.superGuest.ID, f.superHome.ID, map[string]any{"full_name": "Peer edit"}))
	})

	t.Run("a super admin can still edit their own profile", func(t *testing.T) {
		f := newSuperAdminFixture(t)
		assert.Equal(t, fasthttp.StatusOK, f.update(t, f.orgA, f.superHome.ID, f.superHome.ID, map[string]any{"full_name": "Me"}))
	})
}

// The target lookup must fail closed: if it errors, nothing may proceed, not
// even for a request that would otherwise be allowed.
func TestSuperAdminProtection_FailsClosedWhenTargetLookupErrors(t *testing.T) {
	f := newSuperAdminFixture(t)

	const cb = "test:fail_super_admin_lookup"
	require.NoError(t, f.app.DB.Callback().Query().Before("gorm:query").Register(cb, func(db *gorm.DB) {
		for _, s := range db.Statement.Selects {
			if s == "is_super_admin" { // only the guard's own lookup
				_ = db.AddError(errors.New("simulated lookup failure"))
			}
		}
	}))
	t.Cleanup(func() { _ = f.app.DB.Callback().Query().Remove(cb) })

	// Plain-user target, org admin actor: normally allowed, now refused.
	assert.Equal(t, fasthttp.StatusInternalServerError, f.del(t, f.orgA, f.adminA.ID, f.plainA.ID))
	assert.Equal(t, fasthttp.StatusInternalServerError, f.update(t, f.orgA, f.adminA.ID, f.plainA.ID, map[string]any{"full_name": "x"}))
	require.True(t, f.userExists(t, f.plainA.ID), "account must not be deleted")
	var got models.User
	require.NoError(t, f.app.DB.First(&got, "id = ?", f.plainA.ID).Error)
	assert.Equal(t, f.plainA.FullName, got.FullName)

	// Super admin target: still untouched.
	assert.Equal(t, fasthttp.StatusInternalServerError, f.del(t, f.orgA, f.adminA.ID, f.superHome.ID))
	f.assertUntouched(t, f.superHome, 1)
	f.assertNoAudit(t, f.superHome.ID)
}

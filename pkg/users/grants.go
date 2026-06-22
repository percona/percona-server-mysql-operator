package users

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/pkg/errors"
)

// grant represents a single privilege as observed in
// information_schema.USER_PRIVILEGES / SCHEMA_PRIVILEGES. An empty db means a
// global (*.*) privilege.
type grant struct {
	db        string
	privilege string
	grantable bool
}

// scopeSQL renders the ON target for a grant scope, matching the format used by
// UpsertUser: "*.*" for a global privilege and "<db>.*" for a database one.
func scopeSQL(db string) string {
	if db == "" {
		return "*.*"
	}
	return db + ".*"
}

// staleGrantRevokes computes the REVOKE statements required to bring the
// privileges of user@host in line with the desired state in user. It only ever
// revokes privileges that were actually observed (passed in actual), so the
// generated statements never fail with "no such grant".
func staleGrantRevokes(user *apiv1.User, host string, actual []grant) []string {
	desiredPrivs := make(map[string]struct{})
	allPrivs := false
	for _, g := range user.Grants {
		for p := range strings.SplitSeq(g, ",") {
			p = strings.ToUpper(strings.TrimSpace(p))
			if p == "" {
				continue
			}
			if p == "ALL" || p == "ALL PRIVILEGES" {
				allPrivs = true
			}
			desiredPrivs[p] = struct{}{}
		}
	}
	hasGrants := len(desiredPrivs) > 0

	desiredDBs := make(map[string]struct{}, len(user.DBs))
	for _, db := range user.DBs {
		desiredDBs[db] = struct{}{}
	}
	// UpsertUser grants globally only when no databases are specified.
	globalDesired := len(desiredDBs) == 0

	// scopeDesired reports whether a scope is part of the desired state and so
	// its privileges should be reconciled rather than fully stripped.
	scopeDesired := func(db string) bool {
		if !hasGrants {
			return false
		}
		if db == "" {
			return globalDesired
		}
		_, ok := desiredDBs[db]
		return ok
	}

	type scopeRevoke struct {
		privs    []string
		grantOpt bool
	}
	revokes := make(map[string]*scopeRevoke)
	revokeFor := func(db string) *scopeRevoke {
		key := scopeSQL(db)
		s, ok := revokes[key]
		if !ok {
			s = &scopeRevoke{}
			revokes[key] = s
		}
		return s
	}

	for _, g := range actual {
		priv := strings.ToUpper(strings.TrimSpace(g.privilege))
		if priv == "USAGE" {
			// USAGE is the "no privileges" placeholder; it cannot be revoked.
			continue
		}

		if scopeDesired(g.db) {
			// Desired scope: keep everything when ALL is requested, otherwise
			// drop only privileges no longer desired. Grant option is left
			// intact because the remaining desired privileges keep it.
			if _, ok := desiredPrivs[priv]; allPrivs || ok {
				continue
			}
			s := revokeFor(g.db)
			s.privs = append(s.privs, priv)
			continue
		}

		// Scope is no longer desired: strip every observed privilege and, if it
		// was grantable, the grant option too.
		s := revokeFor(g.db)
		s.privs = append(s.privs, priv)
		if g.grantable {
			s.grantOpt = true
		}
	}

	scopes := make([]string, 0, len(revokes))
	for scope := range revokes {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)

	var out []string
	for _, scope := range scopes {
		s := revokes[scope]
		if len(s.privs) > 0 {
			sort.Strings(s.privs)
			out = append(out, fmt.Sprintf("REVOKE %s ON %s FROM '%s'@'%s'",
				strings.Join(s.privs, ", "), scope, escapeIdentifier(user.Name), escapeIdentifier(host)))
		}
		if s.grantOpt {
			out = append(out, fmt.Sprintf("REVOKE GRANT OPTION ON %s FROM '%s'@'%s'",
				scope, escapeIdentifier(user.Name), escapeIdentifier(host)))
		}
	}

	return out
}

// RevokeStaleGrants removes any privilege held by the user that is no longer
// described by the user spec, for each of the user's hosts. It reconciles
// privileges granted by UpsertUser (global and per-database), leaving any
// out-of-band table/column level grants untouched.
func (m *manager) RevokeStaleGrants(ctx context.Context, user *apiv1.User) error {
	hosts := user.Hosts
	if len(hosts) == 0 {
		hosts = []string{"%"}
	}

	for _, host := range hosts {
		actual, err := m.observedGrants(ctx, user.Name, host)
		if err != nil {
			return errors.Wrapf(err, "get grants for %s@%s", user.Name, host)
		}

		for _, q := range staleGrantRevokes(user, host, actual) {
			if _, err := m.db.ExecContext(ctx, q); err != nil {
				return errors.Wrapf(err, "revoke stale grants for %s@%s", user.Name, host)
			}
		}
	}

	return nil
}

// observedGrants returns the global and database-level privileges currently
// held by name@host, as recorded in the information_schema privilege tables.
func (m *manager) observedGrants(ctx context.Context, name, host string) ([]grant, error) {
	grantee := fmt.Sprintf("'%s'@'%s'", escapeIdentifier(name), escapeIdentifier(host))

	var grants []grant

	globalRows, err := m.db.QueryContext(ctx,
		"SELECT PRIVILEGE_TYPE, IS_GRANTABLE FROM information_schema.USER_PRIVILEGES WHERE GRANTEE = ?", grantee)
	if err != nil {
		return nil, errors.Wrap(err, "query user privileges")
	}
	defer globalRows.Close()
	for globalRows.Next() {
		var priv, grantable string
		if err := globalRows.Scan(&priv, &grantable); err != nil {
			return nil, errors.Wrap(err, "scan user privilege")
		}
		grants = append(grants, grant{privilege: priv, grantable: grantable == "YES"})
	}
	if err := globalRows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate user privileges")
	}

	schemaRows, err := m.db.QueryContext(ctx,
		"SELECT TABLE_SCHEMA, PRIVILEGE_TYPE, IS_GRANTABLE FROM information_schema.SCHEMA_PRIVILEGES WHERE GRANTEE = ?", grantee)
	if err != nil {
		return nil, errors.Wrap(err, "query schema privileges")
	}
	defer schemaRows.Close()
	for schemaRows.Next() {
		var db, priv, grantable string
		if err := schemaRows.Scan(&db, &priv, &grantable); err != nil {
			return nil, errors.Wrap(err, "scan schema privilege")
		}
		grants = append(grants, grant{db: db, privilege: priv, grantable: grantable == "YES"})
	}
	if err := schemaRows.Err(); err != nil {
		return nil, errors.Wrap(err, "iterate schema privileges")
	}

	return grants, nil
}

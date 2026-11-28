package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/yamdrake/internal/audit"
	"github.com/rcss-labs/yamdrake/internal/auth"
	"github.com/rcss-labs/yamdrake/internal/store"
)

const (
	maxNameLength      = 200
	maxGroupNameLength = 100
	maxIDPGroupLength  = 255
	defaultAuditLimit  = 50
	maxAuditLimit      = 200
)

// errLastAdmin is returned when a change would leave no enabled user in an
// admin group.
var errLastAdmin = errors.New("at least one enabled user must stay in an admin group")

// writeAccessError maps the store's sentinels to API answers. The messages of
// store.ErrInvalid describe a rule and never repeat a secret.
func (s *server) writeAccessError(w http.ResponseWriter, r *http.Request, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		notFound(w, what)
	case errors.Is(err, errLastAdmin):
		writeError(w, http.StatusConflict, codeConflict, errLastAdmin.Error())
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, codeConflict, what+" already exists")
	case errors.Is(err, store.ErrInvalidReference):
		invalid(w, "the request names a group or folder that does not exist")
	case errors.Is(err, store.ErrInvalid):
		invalid(w, err.Error())
	default:
		s.internalError(w, r, err)
	}
}

// guardAdmins runs change under the admin lock and refuses it (so the
// transaction rolls back) when it takes the last enabled administrator away.
// The lock is held to the end of the transaction, so a concurrent change that
// could also remove an administrator waits and then counts afresh.
func guardAdmins(ctx context.Context, tx pgx.Tx, change func() error) error {
	if err := store.LockAdminChanges(ctx, tx); err != nil {
		return err
	}
	before, err := store.CountEnabledAdmins(ctx, tx)
	if err != nil {
		return err
	}
	if err := change(); err != nil {
		return err
	}
	after, err := store.CountEnabledAdmins(ctx, tx)
	if err != nil {
		return err
	}
	if before > 0 && after == 0 {
		return errLastAdmin
	}
	return nil
}

// ---- users ----

// groupRef is a group as listed on a user.
type groupRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Source is "manual" when an administrator put the user in the group and
	// "idp" when only the identity provider did. A user in a group both ways
	// reports "manual": that membership survives removal at the provider, and
	// it is the one a PATCH of group_ids can change.
	Source string `json:"source"`
}

type groupView struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Admin       bool   `json:"admin"`
	MemberCount int    `json:"member_count"`
}

func groupViewOf(g store.Group, members int) groupView {
	return groupView{ID: g.ID, Name: g.Name, Admin: g.Admin, MemberCount: members}
}

type userResponse struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	Source       string     `json:"source"`
	Disabled     bool       `json:"disabled"`
	LastSignInAt *time.Time `json:"last_sign_in_at"`
	CreatedAt    time.Time  `json:"created_at"`
	Groups       []groupRef `json:"groups"`
	// SessionsRevoked is set by a PATCH that disables the user: false means
	// the sessions could not be deleted (Valkey is down). The user is refused
	// on every request regardless, so the disable stands.
	SessionsRevoked *bool `json:"sessions_revoked,omitempty"`
}

func (s *server) userView(ctx context.Context, u store.User) (userResponse, error) {
	groups, err := store.GroupsOfUser(ctx, s.Pool, u.ID)
	if err != nil {
		return userResponse{}, err
	}
	manualIDs, err := store.ManualGroupIDs(ctx, s.Pool, u.ID)
	if err != nil {
		return userResponse{}, err
	}
	manual := make(map[int64]bool, len(manualIDs))
	for _, id := range manualIDs {
		manual[id] = true
	}
	out := userResponse{ID: u.ID, Email: u.Email, Name: u.Name, Source: u.Source, Disabled: u.Disabled,
		LastSignInAt: u.LastLoginAt, CreatedAt: u.CreatedAt, Groups: []groupRef{}}
	for _, g := range groups {
		source := "idp"
		if manual[g.ID] {
			source = "manual"
		}
		out.Groups = append(out.Groups, groupRef{ID: g.ID, Name: g.Name, Source: source})
	}
	return out, nil
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := store.ListUsers(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		view, err := s.userView(r.Context(), u)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Source   string `json:"source"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	name := strings.TrimSpace(req.Name)
	if parsed, err := mail.ParseAddress(email); err != nil || parsed.Address != email || len(email) > maxEmailLength {
		invalid(w, "email must be a plain email address")
		return
	}
	if len(name) > maxNameLength {
		invalid(w, "name is too long")
		return
	}
	if req.Source != "local" && req.Source != "service" {
		invalid(w, `source must be "local" or "service"`)
		return
	}
	if len(req.Password) > maxPasswordLength {
		invalid(w, "password is too long")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if errors.Is(err, auth.ErrWeakPassword) {
		invalid(w, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var created store.User
	err = s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var err error
		created, err = store.CreateLocalUser(ctx, tx, email, name, req.Source, hash)
		return audit.Event{Action: "user.create", TargetType: "user", TargetID: idString(created.ID),
			Detail: map[string]any{"email": email, "name": name, "source": req.Source}}, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, fmt.Sprintf("user %q", email))
		return
	}
	view, err := s.userView(r.Context(), created)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func idString(n int64) string { return strconv.FormatInt(n, 10) }

func (s *server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(w, "user")
		return
	}
	var req struct {
		Disabled       *bool    `json:"disabled"`
		ManualGroupIDs *[]int64 `json:"group_ids"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Disabled == nil && req.ManualGroupIDs == nil {
		invalid(w, "disabled or group_ids is required")
		return
	}
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var event audit.Event
		err := guardAdmins(ctx, tx, func() error {
			user, err := store.UserByID(ctx, tx, id)
			if err != nil {
				return err
			}
			beforeManual, err := store.ManualGroupIDs(ctx, tx, id)
			if err != nil {
				return err
			}
			before := map[string]any{"disabled": user.Disabled, "group_ids": beforeManual}
			after := map[string]any{"disabled": user.Disabled, "group_ids": beforeManual}
			if req.Disabled != nil {
				if err := store.SetDisabled(ctx, tx, id, *req.Disabled); err != nil {
					return err
				}
				after["disabled"] = *req.Disabled
			}
			if req.ManualGroupIDs != nil {
				wanted := slices.Clone(*req.ManualGroupIDs)
				slices.Sort(wanted)
				wanted = slices.Compact(wanted)
				for _, groupID := range wanted {
					if err := store.SetManualMembership(ctx, tx, id, groupID, true); err != nil {
						return err
					}
				}
				for _, groupID := range beforeManual {
					if !slices.Contains(wanted, groupID) {
						if err := store.SetManualMembership(ctx, tx, id, groupID, false); err != nil {
							return err
						}
					}
				}
				after["group_ids"] = wanted
			}
			event = audit.Event{Action: "user.update", TargetType: "user", TargetID: idString(id),
				Detail: map[string]any{"email": user.Email, "before": before, "after": after}}
			return nil
		})
		return event, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, "user")
		return
	}
	// The disable is committed and enforced on every request (Resolve refuses
	// a disabled user), so sessions are deleted before anything that can fail
	// (reading the user back). A Valkey failure only leaves stale sessions that
	// can no longer be used: say so in the response, log without session ids,
	// and still answer 200.
	var sessionsDeleted *bool
	if req.Disabled != nil && *req.Disabled {
		deleted := s.deleteUserSessions(r, id)
		sessionsDeleted = &deleted
	}
	user, err := store.UserByID(r.Context(), s.Pool, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	view, err := s.userView(r.Context(), user)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	view.SessionsRevoked = sessionsDeleted
	writeJSON(w, http.StatusOK, view)
}

func (s *server) deleteUserSessions(r *http.Request, userID int64) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), valkeyTimeout)
	defer cancel()
	if err := s.Sessions.DeleteAllForUser(ctx, userID); err != nil {
		s.Log.Error("delete sessions of a disabled user failed; the user is refused on every request anyway",
			"user_id", userID, "error", err.Error())
		return false
	}
	return true
}

// ---- groups ----

func (s *server) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := store.ListGroups(r.Context(), s.Pool)
	if err == nil {
		var counts map[int64]int
		if counts, err = store.GroupMemberCounts(r.Context(), s.Pool); err == nil {
			out := make([]groupView, 0, len(groups))
			for _, g := range groups {
				out = append(out, groupViewOf(g, counts[g.ID]))
			}
			writeJSON(w, http.StatusOK, map[string]any{"groups": out})
			return
		}
	}
	s.internalError(w, r, err)
}

func (s *server) createGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Admin bool   `json:"admin"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxGroupNameLength {
		invalid(w, fmt.Sprintf("name is required and at most %d characters", maxGroupNameLength))
		return
	}
	var created store.Group
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var err error
		created, err = store.CreateGroup(ctx, tx, name, req.Admin)
		return audit.Event{Action: "group.create", TargetType: "group", TargetID: idString(created.ID),
			Detail: map[string]any{"name": name, "admin": req.Admin}}, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, fmt.Sprintf("group %q", name))
		return
	}
	writeJSON(w, http.StatusCreated, groupViewOf(created, 0))
}

func (s *server) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(w, "group")
		return
	}
	var req struct {
		Admin *bool `json:"admin"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Admin == nil {
		invalid(w, "admin is required")
		return
	}
	var updated store.Group
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var event audit.Event
		err := guardAdmins(ctx, tx, func() error {
			groups, err := store.ListGroups(ctx, tx)
			if err != nil {
				return err
			}
			index := slices.IndexFunc(groups, func(g store.Group) bool { return g.ID == id })
			if index < 0 {
				return store.ErrNotFound
			}
			updated = groups[index]
			if err := store.SetGroupAdmin(ctx, tx, id, *req.Admin); err != nil {
				return err
			}
			event = audit.Event{Action: "group.update", TargetType: "group", TargetID: idString(id),
				Detail: map[string]any{"name": updated.Name,
					"before": map[string]any{"admin": updated.Admin}, "after": map[string]any{"admin": *req.Admin}}}
			updated.Admin = *req.Admin
			return nil
		})
		return event, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, "group")
		return
	}
	counts, err := store.GroupMemberCounts(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, groupViewOf(updated, counts[id]))
}

// ---- identity-provider group mappings ----

type mappingView struct {
	IDPGroup string `json:"idp_group"`
	GroupID  int64  `json:"group_id"`
}

func (s *server) listIDPMappings(w http.ResponseWriter, r *http.Request) {
	mappings, err := store.ListIDPMappings(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]mappingView, 0, len(mappings))
	for name, groupID := range mappings {
		out = append(out, mappingView{IDPGroup: name, GroupID: groupID})
	}
	slices.SortFunc(out, func(a, b mappingView) int { return strings.Compare(a.IDPGroup, b.IDPGroup) })
	writeJSON(w, http.StatusOK, map[string]any{"mappings": out})
}

// idpGroupName reads and checks the {idpGroup} path value.
func idpGroupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("idpGroup")
	if name == "" || len(name) > maxIDPGroupLength {
		invalid(w, fmt.Sprintf("the identity-provider group name is required and at most %d characters", maxIDPGroupLength))
		return "", false
	}
	return name, true
}

func (s *server) getIDPMapping(w http.ResponseWriter, r *http.Request) {
	name, ok := idpGroupName(w, r)
	if !ok {
		return
	}
	mappings, err := store.ListIDPMappings(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	groupID, found := mappings[name]
	if !found {
		notFound(w, "mapping")
		return
	}
	writeJSON(w, http.StatusOK, mappingView{IDPGroup: name, GroupID: groupID})
}

func (s *server) putIDPMapping(w http.ResponseWriter, r *http.Request) {
	name, ok := idpGroupName(w, r)
	if !ok {
		return
	}
	var req struct {
		GroupID int64 `json:"group_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.GroupID <= 0 {
		invalid(w, "group_id is required")
		return
	}
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		mappings, err := store.ListIDPMappings(ctx, tx)
		if err != nil {
			return audit.Event{}, err
		}
		before := any(nil)
		if previous, had := mappings[name]; had {
			before = previous
		}
		err = store.SetIDPMapping(ctx, tx, name, req.GroupID)
		return audit.Event{Action: "idp_mapping.set", TargetType: "idp_mapping", TargetID: name,
			Detail: map[string]any{"before": map[string]any{"group_id": before}, "after": map[string]any{"group_id": req.GroupID}}}, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, "mapping")
		return
	}
	writeJSON(w, http.StatusOK, mappingView{IDPGroup: name, GroupID: req.GroupID})
}

func (s *server) deleteIDPMapping(w http.ResponseWriter, r *http.Request) {
	name, ok := idpGroupName(w, r)
	if !ok {
		return
	}
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		mappings, err := store.ListIDPMappings(ctx, tx)
		if err != nil {
			return audit.Event{}, err
		}
		if err := store.DeleteIDPMapping(ctx, tx, name); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "idp_mapping.delete", TargetType: "idp_mapping", TargetID: name,
			Detail: map[string]any{"before": map[string]any{"group_id": mappings[name]}}}, nil
	})
	if err != nil {
		s.writeAccessError(w, r, err, "mapping")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- grants ----

type grantResponse struct {
	ID          int64  `json:"id"`
	GroupID     int64  `json:"group_id"`
	Action      string `json:"action"`
	Label       string `json:"label"`
	Environment string `json:"environment"`
	FolderID    int64  `json:"folder_id"`
}

func grantResponseOf(g store.Grant) grantResponse {
	return grantResponse{ID: g.ID, GroupID: g.GroupID, Action: g.Action, Label: g.Label, Environment: g.Environment, FolderID: g.FolderID}
}

func grantDetail(g store.Grant) map[string]any {
	return map[string]any{"group_id": g.GroupID, "action": g.Action, "label": g.Label, "environment": g.Environment, "folder_id": g.FolderID}
}

func (s *server) listGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := store.ListGrants(r.Context(), s.Pool)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]grantResponse, 0, len(grants))
	for _, g := range grants {
		out = append(out, grantResponseOf(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": out})
}

func (s *server) createGrant(w http.ResponseWriter, r *http.Request) {
	var req grantResponse
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ID != 0 {
		invalid(w, "id is assigned by the server")
		return
	}
	var created store.Grant
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		var err error
		created, err = store.CreateGrant(ctx, tx, store.Grant{GroupID: req.GroupID, Action: req.Action,
			Label: req.Label, Environment: req.Environment, FolderID: req.FolderID})
		return audit.Event{Action: "grant.create", TargetType: "grant", TargetID: idString(created.ID), Detail: grantDetail(created)}, err
	})
	if err != nil {
		s.writeAccessError(w, r, err, "grant")
		return
	}
	writeJSON(w, http.StatusCreated, grantResponseOf(created))
}

func (s *server) deleteGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		notFound(w, "grant")
		return
	}
	err := s.auditedWrite(r, func(ctx context.Context, tx pgx.Tx) (audit.Event, error) {
		grants, err := store.ListGrants(ctx, tx)
		if err != nil {
			return audit.Event{}, err
		}
		index := slices.IndexFunc(grants, func(g store.Grant) bool { return g.ID == id })
		if index < 0 {
			return audit.Event{}, store.ErrNotFound
		}
		if err := store.DeleteGrant(ctx, tx, id); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "grant.delete", TargetType: "grant", TargetID: idString(id),
			Detail: map[string]any{"before": grantDetail(grants[index])}}, nil
	})
	if err != nil {
		s.writeAccessError(w, r, err, "grant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- audit log ----

type auditEntryResponse struct {
	ID         int64           `json:"id"`
	At         time.Time       `json:"at"`
	ActorID    *int64          `json:"actor_id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

// listAudit pages through the log, newest first. before is the id of the last
// entry of the previous page; a limit above 200 is capped to 200.
func (s *server) listAudit(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	var before int64
	if raw := query.Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			invalid(w, "before must be a non-negative entry id")
			return
		}
		before = parsed
	}
	limit := defaultAuditLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			invalid(w, "limit must be a positive number")
			return
		}
		limit = min(parsed, maxAuditLimit)
	}
	// One row more than asked for tells whether an older page exists.
	entries, err := store.ListAudit(r.Context(), s.Pool, before, limit+1)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	hasOlder := len(entries) > limit
	if hasOlder {
		entries = entries[:limit]
	}
	out := make([]auditEntryResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, auditEntryResponse{ID: e.ID, At: e.At, ActorID: e.ActorID, Actor: e.Actor, Action: e.Action,
			TargetType: e.TargetType, TargetID: e.TargetID, Detail: e.Detail})
	}
	// next_before is null on the last page.
	var nextBefore *int64
	if hasOlder {
		nextBefore = &entries[len(entries)-1].ID
	}
	body := map[string]any{"entries": out, "next_before": nextBefore}
	writeJSON(w, http.StatusOK, body)
}

// accessRoutes lists the access administration, token and device sign-in
// routes of the authenticated mux. Each states the action it needs.
func (s *server) accessRoutes() []routeDef {
	return []routeDef{
		{"GET /api/v1/admin/users", auth.ManageAccess, s.listUsers},
		{"POST /api/v1/admin/users", auth.ManageAccess, s.createUser},
		{"PATCH /api/v1/admin/users/{id}", auth.ManageAccess, s.updateUser},
		{"GET /api/v1/admin/groups", auth.ManageAccess, s.listGroups},
		{"POST /api/v1/admin/groups", auth.ManageAccess, s.createGroup},
		{"PATCH /api/v1/admin/groups/{id}", auth.ManageAccess, s.updateGroup},
		{"GET /api/v1/admin/idp-mappings", auth.ManageAccess, s.listIDPMappings},
		{"GET /api/v1/admin/idp-mappings/{idpGroup}", auth.ManageAccess, s.getIDPMapping},
		{"PUT /api/v1/admin/idp-mappings/{idpGroup}", auth.ManageAccess, s.putIDPMapping},
		{"DELETE /api/v1/admin/idp-mappings/{idpGroup}", auth.ManageAccess, s.deleteIDPMapping},
		{"GET /api/v1/admin/grants", auth.ManageAccess, s.listGrants},
		{"POST /api/v1/admin/grants", auth.ManageAccess, s.createGrant},
		{"DELETE /api/v1/admin/grants/{id}", auth.ManageAccess, s.deleteGrant},
		{"GET /api/v1/admin/audit", auth.ViewAudit, s.listAudit},
		// Every signed-in principal has View; these touch only the caller's own data.
		{"GET /api/v1/tokens", auth.View, s.listTokens},
		{"POST /api/v1/tokens", auth.View, s.createToken},
		{"DELETE /api/v1/tokens/{id}", auth.View, s.revokeToken},
		// Session principals only: the handler answers 403 to a bearer token.
		{"POST /api/v1/cli/login/confirm", auth.View, s.confirmDeviceLogin},
	}
}

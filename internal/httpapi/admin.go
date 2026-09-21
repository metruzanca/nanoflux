package httpapi

import (
	"net/http"
	"strconv"

	"github.com/charmbracelet/log"

	"github.com/metruzanca/nanoflux/internal/auth"
	"github.com/metruzanca/nanoflux/internal/web"
)

type adminData struct {
	Users []adminUserRow
}

type adminUserRow struct {
	ID        int64
	Username  string
	IsAdmin   bool
	IsSelf    bool
	CreatedAt string
	Timezone  string
}

// adminOnly guards admin routes: only users with the admin flag may pass.
// Everyone else gets a visible 403 (a page for GET, an error fragment for htmx
// POSTs) so the denial is never silent.
func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.UserFrom(r)
		if ok && u.IsAdmin {
			next.ServeHTTP(w, r)
			return
		}
		log.Warn("admin route denied", "path", r.URL.Path)
		if r.Method != http.MethodGet {
			writeFormError(w, r, "admin-error", "admin only")
			return
		}
		w.WriteHeader(http.StatusForbidden)
		web.Render(w, r, basePage("forbidden", u, forbiddenPage("admin only")))
	})
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	web.Render(w, r, basePage("admin", u, adminPage(u, s.adminUsers(u.ID, u.Timezone))))
}

func (s *Server) adminUsers(selfID int64, tz string) adminData {
	users, err := s.store.Users.List()
	if err != nil {
		log.Error("admin: list users", "err", err)
		return adminData{}
	}
	rows := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		rows = append(rows, adminUserRow{
			ID:        u.ID,
			Username:  u.Username,
			IsAdmin:   u.IsAdmin,
			IsSelf:    u.ID == selfID,
			CreatedAt: u.CreatedAt,
			Timezone:  tz,
		})
	}
	return adminData{Users: rows}
}

// adminResetPassword sets a new password for a user and logs out all of their
// sessions.
func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	userID, ok := adminUserID(w, r)
	if !ok {
		return
	}
	pw := r.FormValue("password")
	if len(pw) < 8 {
		writeFormError(w, r, adminErrorTarget(userID), "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		log.Error("admin: hash password", "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not reset password")
		return
	}
	if err := s.store.Users.ResetPassword(userID, hash); err != nil {
		log.Error("admin: reset password", "user_id", userID, "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not reset password")
		return
	}
	if err := s.store.Sessions.DeleteUserSessions(userID); err != nil {
		log.Error("admin: revoke sessions", "user_id", userID, "err", err)
	}
	web.Render(w, r, AdminUserList(s.adminUsers(u.ID, u.Timezone).Users))
}

// adminSetAdmin grants or revokes the admin flag. The last remaining admin
// cannot be demoted.
func (s *Server) adminSetAdmin(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	userID, ok := adminUserID(w, r)
	if !ok {
		return
	}
	admin := r.FormValue("admin") == "true"
	target, err := s.store.Users.ByID(userID)
	if err != nil {
		writeFormError(w, r, adminErrorTarget(userID), "no such user")
		return
	}
	if !admin && target.IsAdmin {
		admins, err := s.store.Users.CountAdmins()
		if err != nil {
			log.Error("admin: count admins", "err", err)
			writeFormError(w, r, adminErrorTarget(userID), "could not update admin flag")
			return
		}
		if admins <= 1 {
			writeFormError(w, r, adminErrorTarget(userID), "cannot remove the last admin")
			return
		}
	}
	if err := s.store.Users.SetAdmin(userID, admin); err != nil {
		log.Error("admin: set admin", "user_id", userID, "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not update admin flag")
		return
	}
	web.Render(w, r, AdminUserList(s.adminUsers(u.ID, u.Timezone).Users))
}

// adminDeleteUser removes a user and their data, purging avatar/icon blobs
// from object storage. The current user and the last admin are protected.
func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r)
	userID, ok := adminUserID(w, r)
	if !ok {
		return
	}
	if userID == u.ID {
		writeFormError(w, r, adminErrorTarget(userID), "cannot delete your own account")
		return
	}
	target, err := s.store.Users.ByID(userID)
	if err != nil {
		writeFormError(w, r, adminErrorTarget(userID), "no such user")
		return
	}
	if target.IsAdmin {
		admins, err := s.store.Users.CountAdmins()
		if err != nil {
			log.Error("admin: count admins", "err", err)
			writeFormError(w, r, adminErrorTarget(userID), "could not delete user")
			return
		}
		if admins <= 1 {
			writeFormError(w, r, adminErrorTarget(userID), "cannot delete the last admin")
			return
		}
	}
	// Purge object-storage blobs best-effort: the DB row is the source of
	// truth, so a flaky store must not block deletion.
	keys, err := s.store.Users.ListObjectKeys(userID)
	if err == nil {
		for _, k := range keys {
			if err := s.files.Delete(r.Context(), k); err != nil {
				log.Warn("admin: purge object", "key", k, "err", err)
			}
		}
	}
	if err := s.store.Users.Delete(userID); err != nil {
		log.Error("admin: delete user", "user_id", userID, "err", err)
		writeFormError(w, r, adminErrorTarget(userID), "could not delete user")
		return
	}
	web.Render(w, r, AdminUserList(s.adminUsers(u.ID, u.Timezone).Users))
}

// adminUserID parses the {id} path value, writing a 400 error fragment and
// returning false when it is missing or not a number.
func adminUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeFormError(w, r, "admin-error", "invalid user")
		return 0, false
	}
	return id, true
}

func adminErrorTarget(userID int64) string {
	return "admin-user-" + strconv.FormatInt(userID, 10) + "-error"
}

package httpapi

import (
	"net/http"
	"strconv"

	"github.com/charmbracelet/log"
)

// parseID reads the {id} path value as an int64.
func parseID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

// containsListID reports whether ids contains id (used by the list picker to
// pre-check the lists an item already belongs to).
func containsListID(ids []int64, id int64) bool {
	for _, n := range ids {
		if n == id {
			return true
		}
	}
	return false
}

// internalError logs err and responds 500 with a generic message. The web UI
// never leaks internal details to the client.
func internalError(w http.ResponseWriter, msg string, err error) {
	log.Error(msg, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

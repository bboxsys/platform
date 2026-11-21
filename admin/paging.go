package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/bboxsys/platform/apicontract"
)

// The admin list routes answer the complete, sorted collection unless the
// request pages it. A request naming limit or offset (apicontract.Paging:
// limit 1 to MaxPageLimit, offset from 0) gets a ListPage instead, so a
// client that knows about paging can follow next_offset while has_more is
// true, and one that does not (the console, the agent CLI, older SPAs)
// keeps the array it always had. Rows are sorted on both paths, which is
// what makes a page stable between requests.

// ListPage is the paged form of a list route: the page position
// (apicontract.Page) and the rows in it.
type ListPage[T any] struct {
	apicontract.Page
	Items []T `json:"items"`
}

// pagingRequested reports whether the request asks for a page.
func pagingRequested(r *http.Request) bool {
	q := r.URL.Query()
	return q.Has("limit") || q.Has("offset")
}

// pageBounds validates limit and offset. It writes a 400 and returns false
// when a value is not an integer in range, so a typo cannot silently
// answer with the default page.
func pageBounds(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > apicontract.MaxPageLimit {
			http.Error(w, `{"status": "400", "message": "limit must be an integer from 1 to `+strconv.Itoa(apicontract.MaxPageLimit)+`"}`, http.StatusBadRequest)
			return false
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 {
			http.Error(w, `{"status": "400", "message": "offset must be an integer from 0"}`, http.StatusBadRequest)
			return false
		}
	}
	return true
}

// writeList sorts items with less (nil keeps their order) and writes them
// as a JSON array, or as a ListPage when the request pages.
func writeList[T any](w http.ResponseWriter, r *http.Request, items []T, less func(a, b T) bool) {
	if items == nil {
		items = []T{}
	}
	if less != nil {
		sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) })
	}
	if !pagingRequested(r) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
		return
	}
	if !pageBounds(w, r) {
		return
	}
	page := apicontract.Pagination(r, len(items))
	start, end := page.Bounds()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ListPage[T]{Page: page, Items: items[start:end]})
}

// nameOrder compares two (name, id) pairs case-insensitively by name,
// then by id.
func nameOrder(aName, aID, bName, bID string) bool {
	if an, bn := strings.ToLower(aName), strings.ToLower(bName); an != bn {
		return an < bn
	}
	return aID < bID
}

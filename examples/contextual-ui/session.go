package main

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/kartaladev/hmntsk/examples/internal/invoicing"
)

// userCookie holds the demo session: the ID of the signed-in demo user.
//
// THIS IS NOT AUTHENTICATION. Anyone can set any cookie, and signing in asks
// for no password. It stands for the middleware a real host runs, which
// establishes who the caller is before hmntsk is asked anything.
const userCookie = "demo_user"

// erin places orders. She is the demo's own user, not in the invoicing
// directory, so she takes part in no invoice task and never approves her own
// purchase.
const (
	erin            = "erin"
	groupPurchasing = "purchasing"
)

// demoUser is one person the demo can be signed in as.
type demoUser struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Role   string   `json:"role"`
	Groups []string `json:"groups"`
}

// users are the demo's people, in the order the sign-in page lists them.
var users = []demoUser{
	{ID: invoicing.Alice, Name: "Alice Hart", Role: "Finance approver", Groups: []string{invoicing.GroupApprovers}},
	{ID: invoicing.Bob, Name: "Bob Nakamura", Role: "Finance approver", Groups: []string{invoicing.GroupApprovers}},
	{ID: invoicing.Carol, Name: "Carol Diaz", Role: "Finance manager", Groups: []string{invoicing.GroupManagers}},
	{ID: invoicing.Dave, Name: "Dave Okafor", Role: "Auditor", Groups: []string{invoicing.GroupAuditors}},
	{ID: erin, Name: "Erin Walsh", Role: "Purchasing", Groups: []string{groupPurchasing}},
}

func userByID(id string) (demoUser, bool) {
	i := slices.IndexFunc(users, func(u demoUser) bool { return u.ID == id })
	if i < 0 {
		return demoUser{}, false
	}

	return users[i], true
}

// sessionUser is the signed-in demo user, accepting only a known one.
func sessionUser(r *http.Request) (demoUser, bool) {
	cookie, err := r.Cookie(userCookie)
	if err != nil {
		return demoUser{}, false
	}

	return userByID(cookie.Value)
}

// actor is who the task and notification handlers act for: the signed-in demo
// user, or nobody.
func actor(r *http.Request) string {
	user, _ := sessionUser(r)

	return user.ID
}

func listUsers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, users)
}

// requireUser is the signed-in demo user. With nobody signed in it answers 401
// and reports false, so a handler only has to return.
func requireUser(w http.ResponseWriter, r *http.Request) (demoUser, bool) {
	if user, ok := sessionUser(r); ok {
		return user, true
	}

	http.Error(w, "not signed in", http.StatusUnauthorized)

	return demoUser{}, false
}

// session answers who is signed in. The page asks, because the cookie is
// HttpOnly and its script cannot read it.
func session(w http.ResponseWriter, r *http.Request) {
	if user, ok := requireUser(w, r); ok {
		writeJSON(w, http.StatusOK, user)
	}
}

func signIn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		User string `json:"user"`
	}

	if err := decodeJSON(w, r, &req); err != nil {
		http.Error(w, "the body must be JSON naming a user", http.StatusBadRequest)

		return
	}

	user, ok := userByID(req.User)
	if !ok {
		http.Error(w, "unknown demo user", http.StatusBadRequest)

		return
	}

	http.SetCookie(w, sessionCookie(user.ID, 0))
	writeJSON(w, http.StatusOK, user)
}

func signOut(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, sessionCookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

// sessionCookie is HttpOnly like a real session cookie. It is not Secure only
// because the demo serves plain HTTP on the loopback address.
func sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     userCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// decodeJSON reads a small JSON body into v, refusing fields v does not have.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()

	return decoder.Decode(v)
}

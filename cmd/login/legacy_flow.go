package main

import (
	"io"
	"net/http"

	"workbuddy2api/internal/loginsite"
)

// Preserve the local test/integration seam while production uses explicit sites.
func runURL(base, origin, realm, statePath string, client *http.Client, out io.Writer) {
	site := loginsite.ForRealm(realm)
	site.API, site.Origin = base, origin
	runURLForSite(site, statePath, client, out)
}

func runPoll(base, origin, realm, statePath string, client *http.Client, out io.Writer) {
	site := loginsite.ForRealm(realm)
	site.API, site.Origin = base, origin
	runPollForSite(site, statePath, client, out)
}

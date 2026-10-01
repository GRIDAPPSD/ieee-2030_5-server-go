package assembly

import "testing"

func TestCanonicalResourcePath(t *testing.T) {
	t.Parallel()
	for _, href := range []string{"/edev", "/edev/3/frp", "/edev/3/fsa/1/derp/2/derc"} {
		if got, err := canonicalResourcePath(href); err != nil || got != href {
			t.Errorf("canonicalResourcePath(%q) = %q, %v; want it unchanged", href, got, err)
		}
	}
	for _, href := range []string{
		"",
		"/edev/3/../5/frp",
		"/edev/3/./frp",
		"/edev/3/frp/",
		"/edev//3",
		"//host.example/edev/3/frp",
		"https://host.example/edev/3/frp",
		"edev/3/frp",
		"/edev/3/frp?s=0",
		"/edev/3/frp#x",
		"/edev/3/%66rp",
		`/edev/3\frp`,
		"/edev/3/fr\x7fp",
	} {
		if got, err := canonicalResourcePath(href); err == nil {
			t.Errorf("canonicalResourcePath(%q) = %q, nil; want an error", href, got)
		}
	}
}

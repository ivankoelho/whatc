package handlers

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	cookieAccessName  = "whm_access"
	cookieRefreshName = "whm_refresh"
	cookieCSRFName    = "whm_csrf"
	// cookieSessionName carries the stable session id (a random value that grants
	// nothing) so logout can name the session: the refresh cookie is scoped to
	// /api/auth/refresh and never reaches /api/auth/logout.
	cookieSessionName = "whm_sid"
)

func (a *App) setSessionCookie(r *fastglue.Request, sid string) {
	sc := fasthttp.AcquireCookie()
	sc.SetKey(cookieSessionName)
	sc.SetValue(sid)
	sc.SetHTTPOnly(true)
	sc.SetSecure(a.Config.Cookie.Secure)
	sc.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	sc.SetPath(a.Config.Server.BasePath + "/api")
	sc.SetMaxAge(a.Config.JWT.RefreshExpiryDays * 86400)
	if d := a.Config.Cookie.Domain; d != "" {
		sc.SetDomain(d)
	}
	r.RequestCtx.Response.Header.SetCookie(sc)
	fasthttp.ReleaseCookie(sc)
}

// setAuthCookies sets httpOnly auth cookies and a JS-readable CSRF cookie.
func (a *App) setAuthCookies(r *fastglue.Request, accessToken, refreshToken string) {
	secure := a.Config.Cookie.Secure
	domain := a.Config.Cookie.Domain
	bp := a.Config.Server.BasePath // e.g. "/whatomate" or ""

	// Access token cookie — httpOnly, scoped to basePath/api
	ac := fasthttp.AcquireCookie()
	ac.SetKey(cookieAccessName)
	ac.SetValue(accessToken)
	ac.SetHTTPOnly(true)
	ac.SetSecure(secure)
	ac.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	ac.SetPath(bp + "/api")
	ac.SetMaxAge(a.Config.JWT.AccessExpiryMins * 60)
	if domain != "" {
		ac.SetDomain(domain)
	}
	r.RequestCtx.Response.Header.SetCookie(ac)
	fasthttp.ReleaseCookie(ac)

	// Refresh token cookie — httpOnly, narrow path
	rc := fasthttp.AcquireCookie()
	rc.SetKey(cookieRefreshName)
	rc.SetValue(refreshToken)
	rc.SetHTTPOnly(true)
	rc.SetSecure(secure)
	rc.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	rc.SetPath(bp + "/api/auth/refresh")
	rc.SetMaxAge(a.Config.JWT.RefreshExpiryDays * 86400)
	if domain != "" {
		rc.SetDomain(domain)
	}
	r.RequestCtx.Response.Header.SetCookie(rc)
	fasthttp.ReleaseCookie(rc)

	// CSRF token cookie — NOT httpOnly (JS-readable), broad path
	csrfToken := generateCSRFToken()
	cc := fasthttp.AcquireCookie()
	cc.SetKey(cookieCSRFName)
	cc.SetValue(csrfToken)
	cc.SetHTTPOnly(false)
	cc.SetSecure(secure)
	cc.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	cc.SetPath(bp + "/")
	cc.SetMaxAge(a.Config.JWT.RefreshExpiryDays * 86400)
	if domain != "" {
		cc.SetDomain(domain)
	}
	r.RequestCtx.Response.Header.SetCookie(cc)
	fasthttp.ReleaseCookie(cc)
}

// clearAuthCookies expires all auth cookies.
func (a *App) clearAuthCookies(r *fastglue.Request) {
	domain := a.Config.Cookie.Domain

	bp := a.Config.Server.BasePath
	for _, name := range []string{cookieAccessName, cookieRefreshName, cookieCSRFName, cookieSessionName} {
		c := fasthttp.AcquireCookie()
		c.SetKey(name)
		c.SetValue("")
		c.SetMaxAge(-1)
		c.SetHTTPOnly(name != cookieCSRFName)
		c.SetSecure(a.Config.Cookie.Secure)
		c.SetSameSite(fasthttp.CookieSameSiteLaxMode)
		switch name {
		case cookieAccessName, cookieSessionName:
			c.SetPath(bp + "/api")
		case cookieRefreshName:
			c.SetPath(bp + "/api/auth/refresh")
		default:
			c.SetPath(bp + "/")
		}
		if domain != "" {
			c.SetDomain(domain)
		}
		r.RequestCtx.Response.Header.SetCookie(c)
		fasthttp.ReleaseCookie(c)
	}
}

// generateCSRFToken returns 32 random bytes, base64url encoded.
func generateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand.Read failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

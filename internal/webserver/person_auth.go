package webserver

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"go.uber.org/zap"
)

// TokenVerifier checks a person's ID token; *oidcauth.Verifier is one.
type TokenVerifier interface {
	Verify(ctx context.Context, rawIDToken string) (*authn.Claims, error)
}

const (
	callerKey   = "roleProviderCaller"
	mayActAsKey = "roleProviderMayActAs"
)

// personAuthMiddleware authenticates a person signed in at the self-service UI and decides what they may do: the routes under /v1/manage judge every request by the tokens of the person it acts as.
//
// Only an ID token is accepted here, never an API token — the API tokens are for services, which use the rest of /v1, and those routes in turn accept no ID token. So a person who reaches this service through the UI's proxy can maintain the groups they manage and nothing else: resolving somebody's tokens or reading any group's members stays with the services.
//
// adminTokens (e.g. group:root-admin) maintain every local group and may act on behalf of another person with X-Act-As. Whether the actor may act as somebody is judged on the actor's own tokens; what the request may do, on the tokens of the person acted for — an admin acting for a lecturer sees what the lecturer sees.
func personAuthMiddleware(verifier TokenVerifier, svc *groupmgmt.Service, adminTokens []string, log *zap.SugaredLogger) gin.HandlerFunc {
	isAdmin := func(tokens []string) bool {
		return slices.ContainsFunc(tokens, func(t string) bool { return slices.Contains(adminTokens, t) })
	}
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		raw, ok := authn.CutBearerPrefix(header)
		if !ok {
			log.Warnw("manage: missing or invalid Authorization header", "scheme", authn.Scheme(header))
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid Authorization Bearer header"})
			return
		}
		claims, err := verifier.Verify(c.Request.Context(), raw)
		if err != nil {
			// A token nobody can judge right now is not a rejected token: a 401 would send the browser into a sign-in that cannot finish while the provider is away.
			if errors.Is(err, oidcauth.ErrKeysUnavailable) {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "sign-in is temporarily unavailable: the identity provider cannot be reached"})
				return
			}
			log.Warnw("manage: rejected token", zap.Error(err))
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		actor := claims.Identity()
		if actor == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "the token names nobody"})
			return
		}
		own, err := svc.GetUserTokens(c.Request.Context(), actor)
		if err != nil {
			internalError(c, err)
			c.Abort()
			return
		}
		mayActAs := isAdmin(own)
		target, err := authn.ActAs(c.GetHeader(authn.ActAsHeader), actor, mayActAs)
		if err != nil {
			status := http.StatusForbidden
			if errors.Is(err, authn.ErrActAsInvalid) {
				status = http.StatusBadRequest
			}
			c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
			return
		}
		caller := groupmgmt.Caller{Actor: actor, Tokens: own}
		if target != actor {
			if caller.Tokens, err = svc.GetUserTokens(c.Request.Context(), target); err != nil {
				internalError(c, err)
				c.Abort()
				return
			}
			caller.OnBehalfOf = target
			if authn.IsWrite(c.Request.Method) {
				log.Infow("acting on behalf", "actor", actor, "as", target, "method", c.Request.Method, "path", c.Request.URL.Path)
			}
		}
		caller.Admin = isAdmin(caller.Tokens)
		c.Set(callerKey, caller)
		c.Set(mayActAsKey, mayActAs)
		c.Next()
	}
}

// callerOf returns the caller personAuthMiddleware resolved.
func callerOf(c *gin.Context) groupmgmt.Caller {
	caller, _ := c.MustGet(callerKey).(groupmgmt.Caller)
	return caller
}
